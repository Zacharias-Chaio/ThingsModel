// Package runtime manages the model processor and its restartable NATS clients.
package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"thingsmodel/internal/config"
	"thingsmodel/internal/natsclient"
	"thingsmodel/internal/store"
)

// Manager keeps data processing available while its NATS clients are replaced or restarted.
type Manager struct {
	ctx      context.Context
	registry *Registry
	log      *slog.Logger

	mu       sync.RWMutex
	south    *natsclient.SouthClient
	north    *natsclient.NorthClient
	settings config.App // 最近一次加载的配置（含订阅网关列表）

	topologyMu sync.Mutex
	topology   map[string]*natsclient.MessageQueryResp // gatewayID → 最新拓扑缓存

	commandsMu sync.RWMutex
	commands   []CommandRecord // 近期控制下发记录（环形截断）

	restartMu  sync.Mutex
	restarting bool
}

func NewManager(ctx context.Context, settings config.App) (*Manager, error) {
	if err := config.Validate(settings); err != nil {
		return nil, err
	}
	manager := &Manager{ctx: ctx, registry: NewRegistry(), log: slog.Default().With("module", "runtime"), settings: settings, topology: make(map[string]*natsclient.MessageQueryResp)}
	manager.registry.SetStaleAfter(settings.StaleAfter)
	if err := manager.reloadSouth(settings); err != nil {
		manager.log.Warn("启动南向客户端失败，遥测接收暂不可用", "error", err)
	}
	if err := manager.reloadNorth(settings); err != nil {
		manager.log.Warn("启动北向客户端失败，数据扇出暂不可用", "error", err)
	}
	return manager, nil
}

func (m *Manager) Apply(configs []store.DeviceConfig) { m.registry.Apply(configs) }

func (m *Manager) Remove(id string) { m.registry.Remove(id) }

func (m *Manager) Snapshot() []DeviceStatus { return m.registry.Snapshot() }

func (m *Manager) Sources() []SourceDevice { return m.registry.Sources() }

// South 返回当前南向客户端（可能为 nil，未启用或初始化失败），供 API 层调用拓扑查询与控制下发。
func (m *Manager) South() *natsclient.SouthClient {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.south
}

// North 返回当前北向客户端（可能为 nil），供 API 层调用控制直通入口。
func (m *Manager) North() *natsclient.NorthClient {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.north
}

// GatewayTopology 是一个网关的拓扑查询结果（含失败原因，OK=false 时 Topology 为空）。
type GatewayTopology struct {
	GatewayID string                       `json:"gatewayId"`
	OK        bool                         `json:"ok"`
	Message   string                       `json:"message,omitempty"`
	Topology  *natsclient.MessageQueryResp `json:"topology,omitempty"`
}

// BindingCheckItem 是一条绑定引用对照最新拓扑的校验结论。
type BindingCheckItem struct {
	DeviceID     string `json:"deviceId"`
	DeviceName   string `json:"deviceName"`
	Kind         string `json:"kind"` // property | method | alarm
	Key          string `json:"key"`
	Name         string `json:"name"`
	GatewayID    string `json:"gatewayId"`
	ChannelIndex int    `json:"channelIndex"`
	DeviceIndex  int    `json:"deviceIndex"`
	PropertyID   string `json:"propertyId"`
	Status       string `json:"status"` // ok | gateway_unreachable | missing_channel | missing_device | missing_property | unchecked
	Message      string `json:"message,omitempty"`
}

// QueryTopology 查询单个网关的全量拓扑并更新缓存；南向不可用时返回错误。
func (m *Manager) QueryTopology(ctx context.Context, gatewayID string) (*natsclient.MessageQueryResp, error) {
	south := m.South()
	if south == nil {
		return nil, fmt.Errorf("南向客户端未启用或未连接，无法查询网关拓扑")
	}
	response, err := south.QueryTopology(ctx, gatewayID)
	if err != nil {
		return nil, err
	}
	if response.OK {
		m.topologyMu.Lock()
		m.topology[gatewayID] = response
		m.topologyMu.Unlock()
	}
	return response, nil
}

// RefreshTopology 依次查询所有订阅网关的拓扑并更新缓存，返回逐网关结果。
func (m *Manager) RefreshTopology(ctx context.Context) []GatewayTopology {
	m.mu.RLock()
	settings := m.settings
	m.mu.RUnlock()
	results := make([]GatewayTopology, 0, len(settings.DeviceSubscriptions))
	for _, subscription := range settings.DeviceSubscriptions {
		result := GatewayTopology{GatewayID: subscription.GatewayID, OK: true}
		response, err := m.QueryTopology(ctx, subscription.GatewayID)
		if err != nil {
			result.OK = false
			result.Message = err.Error()
		} else if !response.OK {
			result.OK = false
			result.Message = response.Message
		} else {
			result.Topology = response
		}
		results = append(results, result)
	}
	sort.Slice(results, func(i, j int) bool { return results[i].GatewayID < results[j].GatewayID })
	return results
}

// TopologySnapshot 返回当前拓扑缓存（不触发网络请求），用于重启后的快速恢复视图。
func (m *Manager) TopologySnapshot() []GatewayTopology {
	m.topologyMu.Lock()
	defer m.topologyMu.Unlock()
	results := make([]GatewayTopology, 0, len(m.topology))
	for gatewayID, response := range m.topology {
		results = append(results, GatewayTopology{GatewayID: gatewayID, OK: true, Topology: response})
	}
	sort.Slice(results, func(i, j int) bool { return results[i].GatewayID < results[j].GatewayID })
	return results
}

// CheckBindings 刷新拓扑后，把每条已配置绑定与网关最新拓扑比对：
// 通道/设备索引是否仍存在（网关调整挂载顺序会使 device_index 变化）、属性是否仍在点表中。
func (m *Manager) CheckBindings(ctx context.Context) ([]BindingCheckItem, error) {
	fresh := m.RefreshTopology(ctx)
	topologies := make(map[string]*natsclient.MessageQueryResp, len(fresh))
	unreachable := make(map[string]string, len(fresh))
	for _, result := range fresh {
		if result.OK {
			topologies[result.GatewayID] = result.Topology
		} else {
			unreachable[result.GatewayID] = result.Message
		}
	}
	refs := m.registry.BindingRefs()
	items := make([]BindingCheckItem, 0, len(refs))
	for _, ref := range refs {
		item := BindingCheckItem{DeviceID: ref.DeviceID, DeviceName: ref.DeviceName, Kind: ref.Kind, Key: ref.Key, Name: ref.Name,
			GatewayID: ref.GatewayID, ChannelIndex: ref.ChannelIndex, DeviceIndex: ref.DeviceIndex, PropertyID: ref.PropertyID, Status: "ok"}
		topology, reachable := topologies[ref.GatewayID]
		switch {
		case !reachable:
			if reason, failed := unreachable[ref.GatewayID]; failed {
				item.Status, item.Message = "gateway_unreachable", "网关拓扑查询失败: "+reason
			} else {
				item.Status, item.Message = "gateway_unreachable", "网关不在订阅列表或拓扑不可用"
			}
		default:
			item.Status, item.Message = checkRefAgainstTopology(topology, ref)
		}
		items = append(items, item)
	}
	return items, nil
}

// checkRefAgainstTopology 校验一条绑定引用：通道存在 → 设备存在 → 属性仍在点表中。
func checkRefAgainstTopology(topology *natsclient.MessageQueryResp, ref BindingRef) (string, string) {
	var channel *natsclient.ChannelInfo
	for i := range topology.Channels {
		if topology.Channels[i].ChannelIndex == ref.ChannelIndex {
			channel = &topology.Channels[i]
			break
		}
	}
	if channel == nil {
		return "missing_channel", fmt.Sprintf("通道 %d 不存在（网关当前共 %d 个通道）", ref.ChannelIndex, len(topology.Channels))
	}
	var device *natsclient.DeviceInfo
	for i := range channel.Devices {
		if channel.Devices[i].Index == ref.DeviceIndex {
			device = &channel.Devices[i]
			break
		}
	}
	if device == nil {
		return "missing_device", fmt.Sprintf("设备 %d 未挂载在通道 %d（可能挂载顺序已调整）", ref.DeviceIndex, ref.ChannelIndex)
	}
	if len(device.Datasheet) == 0 {
		return "unchecked", "设备点表为空（模型已删除或网关未提供），无法校验属性"
	}
	for _, entry := range device.Datasheet {
		if entry.DataName == ref.PropertyID || entry.DataIndex == ref.PropertyID {
			return "ok", ""
		}
	}
	return "missing_property", fmt.Sprintf("属性 %s 不在设备点表中", ref.PropertyID)
}

// CommandRecord 是一次控制下发的全生命周期记录：
// 受理（REQ/REP accepted/failure）→ 终态（cmdAck success/failure 或超时）。
type CommandRecord struct {
	RequestID       string    `json:"requestId"`
	DeviceID        string    `json:"deviceId"`
	DeviceName      string    `json:"deviceName"`
	MethodKey       string    `json:"methodKey"`
	MethodName      string    `json:"methodName"`
	GatewayID       string    `json:"gatewayId"`
	ChannelIndex    int       `json:"channelIndex"`
	DeviceIndex     int       `json:"deviceIndex"`
	PropertyID      string    `json:"propertyId"`
	PropertyName    string    `json:"propertyName"`
	Value           float64   `json:"value"`
	AcceptedStatus  string    `json:"acceptedStatus"` // accepted | failure
	AcceptedMessage string    `json:"acceptedMessage,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
	FinalStatus     string    `json:"finalStatus,omitempty"` // success | failure
	FinalMessage    string    `json:"finalMessage,omitempty"`
	FinalizedAt     time.Time `json:"finalizedAt,omitempty"`
}

// commandLogLimit 进程内命令记录上限（环形截断，仅近期可查）。
const commandLogLimit = 200

// InvokeMethod 执行一次服务下发：定位设备方法与绑定点位 → 南向 .cmd REQ/REP →
// 记录命令并异步等待 cmdAck 终态。返回受理结果（accepted 表示已入网关引擎写队列）。
func (m *Manager) InvokeMethod(ctx context.Context, deviceID, methodKey string, value float64) (CommandRecord, error) {
	method, ok := m.registry.Method(deviceID, methodKey)
	if !ok {
		return CommandRecord{}, fmt.Errorf("设备 %s 不存在或没有服务 %s", deviceID, methodKey)
	}
	binding := method.Binding
	if binding.GatewayID == "" || binding.PropertyID == "" || binding.ChannelID < 0 || binding.DeviceID < 0 {
		return CommandRecord{}, fmt.Errorf("服务 %s 未绑定下发点位，请先在设备配置中完成绑定", method.Name)
	}
	if method.Type == "number" && method.Validation.Min < method.Validation.Max &&
		(value < method.Validation.Min || value > method.Validation.Max) {
		return CommandRecord{}, fmt.Errorf("工程值需在 %g ~ %g 范围内", method.Validation.Min, method.Validation.Max)
	}
	south := m.South()
	if south == nil {
		return CommandRecord{}, fmt.Errorf("南向客户端未启用或未连接，无法下发控制指令")
	}
	propertyName := m.registry.PointName(binding.GatewayID, binding.ChannelID, binding.DeviceID, binding.PropertyID)
	record := CommandRecord{
		DeviceID: deviceID, MethodKey: methodKey, MethodName: method.Name,
		GatewayID: binding.GatewayID, ChannelIndex: binding.ChannelID, DeviceIndex: binding.DeviceID,
		PropertyID: binding.PropertyID, PropertyName: propertyName, Value: value, CreatedAt: time.Now().UTC(),
	}
	if name, exists := m.registry.DeviceName(deviceID); exists {
		record.DeviceName = name
	}
	ack, final, err := south.SendCommand(ctx, binding.GatewayID, natsclient.MessageCmd{
		ChannelIndex: binding.ChannelID, DeviceIndex: binding.DeviceID, Name: propertyName, Value: value,
	})
	if err != nil {
		record.AcceptedStatus, record.AcceptedMessage = natsclient.AckStatusFailure, err.Error()
		m.appendCommand(record)
		return record, fmt.Errorf("下发失败: %w", err)
	}
	record.RequestID, record.AcceptedStatus, record.AcceptedMessage = ack.RequestID, ack.Status, ack.Message
	m.appendCommand(record)
	if final != nil {
		go m.awaitFinalAck(ack.RequestID, final)
	}
	return record, nil
}

// Command 按请求 ID 查询命令记录（供前端轮询终态）。
func (m *Manager) Command(requestID string) (CommandRecord, bool) {
	m.commandsMu.RLock()
	defer m.commandsMu.RUnlock()
	for i := len(m.commands) - 1; i >= 0; i-- {
		if m.commands[i].RequestID == requestID {
			return m.commands[i], true
		}
	}
	return CommandRecord{}, false
}

// appendCommand 追加命令记录并维持环形上限。
func (m *Manager) appendCommand(record CommandRecord) {
	m.commandsMu.Lock()
	defer m.commandsMu.Unlock()
	m.commands = append(m.commands, record)
	if len(m.commands) > commandLogLimit {
		m.commands = m.commands[len(m.commands)-commandLogLimit:]
	}
}

// finalizeCommand 幂等地写入命令终态（cmdAck 回调与 final 通道竞争时先到先得）。
func (m *Manager) finalizeCommand(requestID, status, message string, at time.Time) {
	m.commandsMu.Lock()
	defer m.commandsMu.Unlock()
	for i := len(m.commands) - 1; i >= 0; i-- {
		if m.commands[i].RequestID == requestID && m.commands[i].FinalStatus == "" {
			m.commands[i].FinalStatus = status
			m.commands[i].FinalMessage = message
			m.commands[i].FinalizedAt = at
			return
		}
	}
}

// awaitFinalAck 等待一条已受理命令的终态（cmdAck 或超时清理）并回写记录。
func (m *Manager) awaitFinalAck(requestID string, final <-chan natsclient.MessageAck) {
	ack := <-final
	m.finalizeCommand(requestID, ack.Status, ack.Message, time.Now().UTC())
	m.log.Info("控制命令终态", "request_id", requestID, "status", ack.Status, "message", ack.Message)
}

// ReloadBus hot-reloads both NATS clients without disturbing HTTP handlers.
// Each client is rebuilt independently so one side never interrupts the other.
func (m *Manager) ReloadBus(settings config.App) error {
	if err := config.Validate(settings); err != nil {
		return err
	}
	m.registry.SetStaleAfter(settings.StaleAfter)
	m.mu.Lock()
	m.settings = settings
	m.mu.Unlock()
	if err := m.reloadSouth(settings); err != nil {
		return err
	}
	return m.reloadNorth(settings)
}

// reloadSouth 重建南向客户端：遥测订阅 + cmdAck 接收 + cmd/query 请求方。
func (m *Manager) reloadSouth(settings config.App) error {
	candidate, err := natsclient.NewSouthClient(m.ctx, settings, m.handleTelemetry)
	if err != nil {
		return err
	}
	m.mu.Lock()
	previous := m.south
	m.south = candidate
	m.mu.Unlock()
	if previous != nil {
		previous.Close()
	}
	m.wireDirectChannel() // 立即重接线，即使后续北向重载失败，存量北向也指向新南向
	m.log.Info("南向客户端配置已加载", "enabled", settings.Subscriber.Enabled, "subscriptions", len(settings.DeviceSubscriptions))
	return nil
}

// reloadNorth 重建北向客户端：归一化数据扇出 + 预留控制直通入口。
func (m *Manager) reloadNorth(settings config.App) error {
	candidate, err := natsclient.NewNorthClient(m.ctx, settings)
	if err != nil {
		return err
	}
	m.mu.Lock()
	previous := m.north
	m.north = candidate
	m.mu.Unlock()
	if previous != nil {
		previous.Close()
	}
	m.wireDirectChannel() // 北向替换后重新注入当前南向 dispatcher
	m.log.Info("北向客户端配置已加载", "enabled", settings.Publisher.Enabled)
	return nil
}

// wireDirectChannel 接线两客户端之间的进程内直通通道（不经 NATS 主题）：
// 北向 SendCommand → 南向 DispatchCommand → 网关 .cmd；
// 网关 cmdAck 终态 → 南向回调 → 北向侧（当前落日志，预留北向回报）。
func (m *Manager) wireDirectChannel() {
	m.mu.RLock()
	south, north := m.south, m.north
	m.mu.RUnlock()
	if south != nil {
		south.OnCmdAck(func(ack natsclient.MessageAck) {
			m.log.Info("控制命令执行回报", "request_id", ack.RequestID,
				"channel", ack.ChannelIndex, "device", ack.DeviceIndex, "status", ack.Status, "message", ack.Message)
			m.finalizeCommand(ack.RequestID, ack.Status, ack.Message, time.Now().UTC())
		})
	}
	if north != nil {
		if south != nil {
			north.SetDispatcher(south)
		}
	}
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
		// 来源缓存已清空：主动查一轮各网关拓扑，绑定页无需等待下一轮采集即可恢复设备目录。
		for _, result := range m.RefreshTopology(m.ctx) {
			if result.OK {
				devices := 0
				for _, channel := range result.Topology.Channels {
					devices += len(channel.Devices)
				}
				m.log.Info("重启后拓扑刷新完成", "gateway", result.GatewayID, "channels", len(result.Topology.Channels), "devices", devices)
			} else {
				m.log.Warn("重启后拓扑刷新失败", "gateway", result.GatewayID, "error", result.Message)
			}
		}
		m.log.Info("物模型运行时重启完成")
	}()
	return true
}

func (m *Manager) Stop() {
	m.mu.Lock()
	south := m.south
	north := m.north
	m.south = nil
	m.north = nil
	m.mu.Unlock()
	if south != nil {
		south.Close()
	}
	if north != nil {
		north.Close()
	}
}

// handleTelemetry 是南向客户端的入口：Registry 摄取归一化后，
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
	publisher := m.north
	m.mu.RUnlock()
	if publisher == nil {
		return
	}
	for _, message := range output {
		publisher.PublishData(normalizeMessage(message))
	}
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
