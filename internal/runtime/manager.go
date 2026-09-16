// Package runtime manages the model processor and its restartable NATS clients.
package runtime

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"thingsmodel/internal/config"
	"thingsmodel/internal/natsclient"
	"thingsmodel/internal/store"
)

// Processor 是归一化之后、发布之前的处理管道扩展点。
// 清洗、聚合、筛选与业务逻辑（如告警生成、存储转发路由）以 stage 形式插入，
// 返回 keep=false 丢弃该条消息，否则以 output 继续向下游传递。
type Processor interface {
	Process(FanoutMessage) (keep bool, output FanoutMessage)
}

// passthroughProcessor 是默认的透传 stage，保持链路语义不变。
type passthroughProcessor struct{}

func (passthroughProcessor) Process(message FanoutMessage) (bool, FanoutMessage) { return true, message }

// Manager keeps data processing available while its NATS clients are replaced or restarted.
type Manager struct {
	ctx         context.Context
	registry    *Registry
	log         *slog.Logger
	processors  []Processor
	passthrough passthroughProcessor

	mu         sync.RWMutex
	subscriber *natsclient.Subscriber
	publisher  *natsclient.Publisher

	restartMu  sync.Mutex
	restarting bool
}

func NewManager(ctx context.Context, settings config.App) (*Manager, error) {
	if err := config.Validate(settings); err != nil {
		return nil, err
	}
	manager := &Manager{ctx: ctx, registry: NewRegistry(), log: slog.Default().With("module", "runtime")}
	manager.registry.SetStaleAfter(settings.StaleAfter)
	if err := manager.reloadSubscriber(settings); err != nil {
		manager.log.Warn("启动数据订阅客户端失败，遥测接收暂不可用", "error", err)
	}
	if err := manager.reloadPublisher(settings); err != nil {
		manager.log.Warn("启动内容发布客户端失败，数据扇出暂不可用", "error", err)
	}
	return manager, nil
}

// UseProcessors 替换处理管道；空列表等价于透传。
func (m *Manager) UseProcessors(processors []Processor) {
	if len(processors) == 0 {
		m.processors = nil
		return
	}
	m.processors = processors
}

func (m *Manager) Apply(configs []store.DeviceConfig) { m.registry.Apply(configs) }

func (m *Manager) Remove(id string) { m.registry.Remove(id) }

func (m *Manager) Snapshot() []DeviceStatus { return m.registry.Snapshot() }

func (m *Manager) Get(id string) (DeviceStatus, bool) { return m.registry.Get(id) }

func (m *Manager) Sources() []SourceDevice { return m.registry.Sources() }

// ReloadBus hot-reloads both NATS clients without disturbing HTTP handlers.
// Each client is rebuilt independently so one side never interrupts the other.
func (m *Manager) ReloadBus(settings config.App) error {
	if err := config.Validate(settings); err != nil {
		return err
	}
	m.registry.SetStaleAfter(settings.StaleAfter)
	if err := m.reloadSubscriber(settings); err != nil {
		return err
	}
	return m.reloadPublisher(settings)
}

func (m *Manager) reloadSubscriber(settings config.App) error {
	candidate, err := natsclient.NewSubscriber(m.ctx, settings, m.handleTelemetry)
	if err != nil {
		return err
	}
	m.mu.Lock()
	previous := m.subscriber
	m.subscriber = candidate
	m.mu.Unlock()
	if previous != nil {
		previous.Close()
	}
	m.log.Info("数据订阅配置已加载", "enabled", settings.Subscriber.Enabled, "subscriptions", len(settings.DeviceSubscriptions))
	return nil
}

func (m *Manager) reloadPublisher(settings config.App) error {
	candidate, err := natsclient.NewPublisher(m.ctx, settings)
	if err != nil {
		return err
	}
	m.mu.Lock()
	previous := m.publisher
	m.publisher = candidate
	m.mu.Unlock()
	if previous != nil {
		previous.Close()
	}
	m.log.Info("内容发布配置已加载", "enabled", settings.Publisher.Enabled)
	return nil
}

// Restart replaces runtime resources asynchronously while the HTTP server stays available.
func (m *Manager) Restart(settings config.App) bool {
	m.restartMu.Lock()
	defer m.restartMu.Unlock()
	if m.restarting {
		return false
	}
	m.restarting = true
	go func() {
		defer func() {
			m.restartMu.Lock()
			m.restarting = false
			m.restartMu.Unlock()
		}()
		m.registry.ResetLiveData()
		if err := m.ReloadBus(settings); err != nil {
			m.log.Error("运行时重启时加载消息客户端失败", "error", err)
			return
		}
		m.log.Info("物模型运行时重启完成")
	}()
	return true
}

func (m *Manager) Stop() {
	m.mu.Lock()
	subscriber := m.subscriber
	publisher := m.publisher
	m.subscriber = nil
	m.publisher = nil
	m.mu.Unlock()
	if subscriber != nil {
		subscriber.Close()
	}
	if publisher != nil {
		publisher.Close()
	}
}

// handleTelemetry 是订阅客户端的入口：Registry 摄取归一化后，
// 依次经过处理管道，最终交给内容发布客户端扇出。
func (m *Manager) handleTelemetry(data natsclient.MessageData, receivedAt time.Time) {
	properties := make(map[string]SourceProperty, len(data.Properties))
	for id, property := range data.Properties {
		properties[id] = SourceProperty{
			Name: property.Name, Unit: property.Unit, Description: property.Description, AccessMode: property.AccessMode,
			Value: property.Value, Timestamp: time.UnixMilli(property.Timestamp).UTC(),
		}
	}
	output := m.registry.Ingest(SourceTelemetry{
		GatewayID: data.GatewayID, GatewaySN: data.GatewaySN, ChannelIndex: data.ChannelIndex, DeviceIndex: data.DeviceIndex,
		DeviceName: data.DeviceName, CommNo: data.CommNo, ModelID: data.ModelID, ModelName: data.ModelName,
		ReceivedAt: receivedAt, Properties: properties,
	})
	m.mu.RLock()
	publisher := m.publisher
	m.mu.RUnlock()
	if publisher == nil {
		return
	}
	for _, message := range output {
		keep, processed := m.runProcessors(message)
		if !keep {
			continue
		}
		publisher.PublishData(normalizeMessage(processed))
	}
}

func (m *Manager) runProcessors(message FanoutMessage) (bool, FanoutMessage) {
	processors := m.processors
	if len(processors) == 0 {
		processors = []Processor{m.passthrough}
	}
	for _, processor := range processors {
		keep, output := processor.Process(message)
		if !keep {
			return false, FanoutMessage{}
		}
		message = output
	}
	return true, message
}

// normalizeMessage converts one fan-out payload to the wire contract.
func normalizeMessage(message FanoutMessage) natsclient.NormalizedData {
	properties := make(map[string]natsclient.NormalizedProperty, len(message.Properties))
	for _, property := range message.Properties {
		properties[property.Key] = natsclient.NormalizedProperty{
			Name: property.Name, Unit: property.Unit, Value: property.Value, Timestamp: property.Timestamp.UnixMilli(), Quality: property.Quality,
		}
	}
	events := make(map[string]natsclient.NormalizedEvent, len(message.Events))
	for _, event := range message.Events {
		events[event.Key] = natsclient.NormalizedEvent{
			Name: event.Name, Level: event.Level, Status: event.Status, Active: event.Active, Timestamp: event.Timestamp.UnixMilli(),
		}
	}
	return natsclient.NormalizedData{
		DeviceID: message.DeviceID, DeviceName: message.DeviceName, TemplateCode: message.TemplateCode,
		TemplateVersion: message.TemplateVersion, DataStatus: message.DataStatus, Properties: properties, Events: events,
		Timestamp: message.Timestamp.UnixMilli(),
	}
}
