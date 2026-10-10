// Package runtime manages the model processor and its restartable NATS clients.
package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"strings"
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

	publishMu       sync.Mutex
	publishers      map[string]*devicePublisher // deviceID → 独立发布节拍
	publishInterval time.Duration               // 当前调度周期（0=关闭）

	appliedMu sync.Mutex
	applied   map[string]store.DeviceConfig // 上次 Apply 的设备配置（model.changed 对比基准；nil=未加载）

	failures *failureThrottler // 控制高频失败聚合节流器

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
	manager := &Manager{ctx: ctx, registry: NewRegistry(), log: slog.Default().With("module", "runtime"), settings: settings, topology: make(map[string]*natsclient.MessageQueryResp), publishers: make(map[string]*devicePublisher), failures: newFailureThrottler()}
	manager.registry.SetStaleAfter(settings.StaleAfter)
	if err := manager.reloadSouth(settings); err != nil {
		manager.log.Warn("启动南向客户端失败，遥测接收暂不可用", "error", err)
	}
	if err := manager.reloadNorth(settings); err != nil {
		manager.log.Warn("启动北向客户端失败，数据扇出暂不可用", "error", err)
	}
	return manager, nil
}

// Apply 同步设备配置到运行时：注册表更新 → 周期调度同步 → 发布 model.changed 提示事件。
// 首次 Apply 为启动基线加载，不发布变更事件。
func (m *Manager) Apply(configs []store.DeviceConfig) {
	m.registry.Apply(configs)
	m.syncPublishSchedule()
	m.emitModelChanges(configs)
}

// Remove 移除设备：注册表删除 → 调度同步 → 发布 model.changed(deleted)。
func (m *Manager) Remove(id string) {
	m.registry.Remove(id)
	m.syncPublishSchedule()
	m.emitModelRemoved(id)
}

func (m *Manager) Snapshot() []DeviceStatus { return m.registry.Snapshot() }

func (m *Manager) Sources() []SourceDevice { return m.registry.Sources() }

// southClient 返回当前南向客户端（可能为 nil，未启用或初始化失败），供拓扑查询与控制下发。
func (m *Manager) southClient() *natsclient.SouthClient {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.south
}

// GatewayTopology 是一个网关的拓扑查询结果（含失败原因，OK=false 时 Topology 为空）。
type GatewayTopology struct {
	GatewayID string                       `json:"gatewayId"`
	OK        bool                         `json:"ok"`
	Message   string                       `json:"message,omitempty"`
	Topology  *natsclient.MessageQueryResp `json:"topology,omitempty"`
}

// BindingCheckItem 是一条属性物理来源引用对照最新拓扑的校验结论。
type BindingCheckItem struct {
	DeviceID     string `json:"deviceId"`
	DeviceName   string `json:"deviceName"`
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
	south := m.southClient()
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

// CheckBindings 刷新拓扑后，把每条属性物理来源引用与网关最新拓扑比对：
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
		item := BindingCheckItem{DeviceID: ref.DeviceID, DeviceName: ref.DeviceName, Key: ref.Key, Name: ref.Name,
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
// 物理目标：受理（REQ/REP accepted/failure）→ 终态（cmdAck success/failure 或超时）；
// 逻辑目标：写入内存缓存后即时终态（success）。
type CommandRecord struct {
	RequestID       string    `json:"requestId"`
	DeviceID        string    `json:"deviceId"`
	DeviceName      string    `json:"deviceName"`
	MethodKey       string    `json:"methodKey"`
	MethodName      string    `json:"methodName"`
	ServiceID       string    `json:"serviceId,omitempty"` // 实例条目 ID（method_N）
	GatewayID       string    `json:"gatewayId"`
	ChannelIndex    int       `json:"channelIndex"`
	DeviceIndex     int       `json:"deviceIndex"`
	PropertyID      string    `json:"propertyId"`
	PropertyName    string    `json:"propertyName"`
	TargetKey       string    `json:"targetKey"`  // 目标属性点位 key
	TargetKind      string    `json:"targetKind"` // physical | logical
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

// InvokeMethod 执行一次服务下发：定位设备方法与目标属性点位，按点位模式分流：
// logical → 写入内存缓存并即时终态；physical → 南向 .cmd REQ/REP 并等待 cmdAck 终态。
func (m *Manager) InvokeMethod(ctx context.Context, deviceID, methodKey string, value float64) (CommandRecord, error) {
	method, target, ok := m.registry.MethodTarget(deviceID, methodKey)
	if !ok {
		return CommandRecord{}, fmt.Errorf("设备 %s 不存在或没有服务 %s", deviceID, methodKey)
	}
	if method.Binding.PropertyKey == "" {
		return CommandRecord{}, fmt.Errorf("服务 %s 未绑定目标属性点位，请先在设备配置中完成绑定", method.Name)
	}
	if err := validateInvokeValue(method, value); err != nil {
		return CommandRecord{}, err
	}
	record := CommandRecord{
		DeviceID: deviceID, MethodKey: methodKey, MethodName: method.Name, ServiceID: method.ID,
		TargetKey: method.Binding.PropertyKey, TargetKind: target.Mode, Value: value, CreatedAt: time.Now().UTC(),
	}
	if name, exists := m.registry.DeviceName(deviceID); exists {
		record.DeviceName = name
	}
	if target.Mode == store.PropertyModeLogical {
		if err := m.registry.WriteLogical(deviceID, target.Key, value, "service"); err != nil {
			return CommandRecord{}, err
		}
		record.FinalStatus = natsclient.AckStatusSuccess
		record.FinalMessage = "已写入逻辑点位"
		record.FinalizedAt = time.Now().UTC()
		m.appendCommand(record)
		return record, nil
	}
	return m.invokePhysical(ctx, record, target, value)
}

// validateInvokeValue 校验下发工程值：number 按范围，status 必须命中某状态值的工程值。
func validateInvokeValue(method store.Method, value float64) error {
	if method.Type == "number" {
		if method.Validation.Min < method.Validation.Max &&
			(value < method.Validation.Min || value > method.Validation.Max) {
			return fmt.Errorf("工程值需在 %g ~ %g 范围内", method.Validation.Min, method.Validation.Max)
		}
		return nil
	}
	for _, description := range method.Descriptions {
		if valuesEqual(description.Value, value) {
			return nil
		}
	}
	return fmt.Errorf("工程值 %v 不在服务 %s 的状态描述中", value, method.Name)
}

// invokePhysical 把值下发到目标属性的物理来源（须单源直连 ept 且可写）。
func (m *Manager) invokePhysical(ctx context.Context, record CommandRecord, target store.Property, value float64) (CommandRecord, error) {
	binding := target.Binding
	if binding.Method != "" && binding.Method != "ept" {
		return CommandRecord{}, fmt.Errorf("目标属性 %s 为聚合点位，不可作为控制目标", target.Name)
	}
	if len(binding.Sources) != 1 {
		return CommandRecord{}, fmt.Errorf("目标属性 %s 需绑定单一来源才能下发", target.Name)
	}
	source := binding.Sources[0]
	if source.GatewayID == "" || source.PropertyID == "" {
		return CommandRecord{}, fmt.Errorf("目标属性 %s 未绑定物理来源", target.Name)
	}
	if accessMode, ok := m.registry.SourceAccessMode(source.GatewayID, source.ChannelID, source.DeviceID, source.PropertyID); ok {
		if !isWritable(accessMode) {
			return CommandRecord{}, fmt.Errorf("目标属性 %s 的物理点位只读(access_mode=%s)，不可下发", target.Name, accessMode)
		}
	}
	south := m.southClient()
	if south == nil {
		return CommandRecord{}, fmt.Errorf("南向客户端未启用或未连接，无法下发控制指令")
	}
	propertyName := m.registry.PointName(source.GatewayID, source.ChannelID, source.DeviceID, source.PropertyID)
	record.GatewayID = source.GatewayID
	record.ChannelIndex = source.ChannelID
	record.DeviceIndex = source.DeviceID
	record.PropertyID = source.PropertyID
	record.PropertyName = propertyName
	ack, final, err := south.SendCommand(ctx, source.GatewayID, natsclient.MessageCmd{
		ChannelIndex: source.ChannelID, DeviceIndex: source.DeviceID, Name: propertyName, Value: value,
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

// isWritable 判定上游点位 access_mode（r/w/rw，大小写不敏感）是否可写。
func isWritable(accessMode string) bool {
	mode := strings.ToLower(strings.TrimSpace(accessMode))
	return strings.Contains(mode, "w")
}

// handleTelemetry 是南向客户端的入口：Registry 摄取并重算内部状态
// （属性归一化、告警评估计时器推进）。北向发布由周期调度器统一承载，此处不扇出。
func (m *Manager) handleTelemetry(data natsclient.MessageData, receivedAt time.Time) {
	properties := make(map[string]SourceProperty, len(data.Properties))
	for id, property := range data.Properties {
		properties[id] = SourceProperty{
			Name: property.Name, Unit: property.Unit, Description: property.Description, AccessMode: property.AccessMode,
			Value: property.Value, Timestamp: time.UnixMilli(property.Timestamp).UTC(),
		}
	}
	m.registry.Ingest(SourceTelemetry{
		GatewayID: data.GatewayID, GatewaySN: data.GatewaySN, ChannelIndex: data.ChannelIndex, DeviceIndex: data.DeviceIndex,
		DeviceName: data.DeviceName, CommNo: data.CommNo, ModelID: data.ModelID, ModelName: data.ModelName,
		ReceivedAt: receivedAt, Properties: properties,
	})
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

// finalizeCommand 幂等地写入命令终态（cmdAck 回调与 final 通道竞争时先到先得）；
// 失败终态同时经北向告警主题发布 thingsmodel.cmd.result（成功静默，契约 §4.2）。
func (m *Manager) finalizeCommand(requestID, status, message string, at time.Time) {
	m.commandsMu.Lock()
	var fired CommandRecord
	found := false
	for i := len(m.commands) - 1; i >= 0; i-- {
		if m.commands[i].RequestID == requestID && m.commands[i].FinalStatus == "" {
			m.commands[i].FinalStatus = status
			m.commands[i].FinalMessage = message
			m.commands[i].FinalizedAt = at
			fired = m.commands[i]
			found = true
			break
		}
	}
	m.commandsMu.Unlock()
	if found && status == natsclient.AckStatusFailure {
		m.publishCommandFailure(fired, message)
	}
}

// publishCommandFailure 发布控制失败终态（code=thingsmodel.cmd.result，仅失败发布）
// 并驱动高频失败聚合节流（thingsmodel.cmd.frequent_failure，每窗口最多一条）。
func (m *Manager) publishCommandFailure(record CommandRecord, message string) {
	m.mu.RLock()
	north := m.north
	m.mu.RUnlock()
	if north == nil {
		return
	}
	north.PublishAlarm(natsclient.AlarmMessage{
		Category: natsclient.AlarmCategorySoftware,
		Kind:     natsclient.AlarmKindEvent,
		Level:    2,
		Software: &natsclient.AlarmSoftware{
			Code:      natsclient.AlarmCodeCmdResult,
			Component: "runtime",
			Message:   message,
			Context: map[string]any{
				"request_id": record.RequestID, "device_id": record.DeviceID,
				"service_key": record.MethodKey, "service_id": record.ServiceID,
				"value": record.Value, "message": message,
			},
		},
	})
	if m.failures.record(record.DeviceID, record.MethodKey, time.Now().UTC()) {
		north.PublishAlarm(natsclient.AlarmMessage{
			Category: natsclient.AlarmCategorySoftware,
			Kind:     natsclient.AlarmKindEvent,
			Level:    2,
			Count:    failureThreshold,
			Software: &natsclient.AlarmSoftware{
				Code:      natsclient.AlarmCodeCmdFrequentFailure,
				Component: "runtime",
				Message:   "同设备同服务控制失败次数达到阈值",
				Context: map[string]any{
					"device_id": record.DeviceID, "service_key": record.MethodKey,
					"window_ms": failureWindow.Milliseconds(),
				},
			},
		})
	}
}

// 高频失败聚合节流常量（契约 §8）：同 (设备, 服务) 10 秒窗口内失败达 5 次发一条聚合告警。
const (
	failureWindow    = 10 * time.Second
	failureThreshold = 5
)

// failureThrottler 按 (设备, 服务) 维度的时间窗失败计数器。
type failureThrottler struct {
	mu      sync.Mutex
	entries map[string]*failureWindowState
}

type failureWindowState struct {
	started time.Time
	count   int
	emitted bool
}

func newFailureThrottler() *failureThrottler {
	return &failureThrottler{entries: make(map[string]*failureWindowState)}
}

// record 记录一次失败；窗口内计数达到阈值且本窗口未发布过聚合告警时返回 true。
func (t *failureThrottler) record(deviceID, methodKey string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	key := deviceID + "|" + methodKey
	state, ok := t.entries[key]
	if !ok || now.Sub(state.started) >= failureWindow {
		state = &failureWindowState{started: now}
		t.entries[key] = state
	}
	state.count++
	if state.count >= failureThreshold && !state.emitted {
		state.emitted = true
		return true
	}
	return false
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
	if err := m.reloadNorth(settings); err != nil {
		return err
	}
	m.syncPublishSchedule() // 周期配置可能变化（publishInterval）
	return nil
}

// reloadSouth 重建南向客户端：遥测订阅 + cmdAck 接收 + cmd/query 请求方。
func (m *Manager) reloadSouth(settings config.App) error {
	candidate, err := natsclient.NewSouthClient(settings, m.handleTelemetry)
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
	m.wireCmdAck() // 立即接上 cmdAck 终态回调，保证重载期间终态不丢
	m.log.Info("南向客户端配置已加载", "enabled", settings.Subscriber.Enabled, "subscriptions", len(settings.DeviceSubscriptions))
	return nil
}

// reloadNorth 重建北向客户端：数据/告警发布 + 查询/控制应答 + 预留控制直通入口。
func (m *Manager) reloadNorth(settings config.App) error {
	candidate, err := natsclient.NewNorthClient(m.ctx, settings, m)
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
	m.log.Info("北向客户端配置已加载", "enabled", settings.Publisher.Enabled)
	return nil
}

// wireCmdAck 把南向 cmdAck 异步终态接到命令记录与失败告警发布：
// 网关回报（或超时清理）→ finalizeCommand → 仅失败时发布 thingsmodel.cmd.result。
func (m *Manager) wireCmdAck() {
	south := m.southClient()
	if south == nil {
		return
	}
	south.OnCmdAck(func(ack natsclient.MessageAck) {
		m.log.Info("控制命令执行回报", "request_id", ack.RequestID,
			"channel", ack.ChannelIndex, "device", ack.DeviceIndex, "status", ack.Status, "message", ack.Message)
		m.finalizeCommand(ack.RequestID, ack.Status, ack.Message, time.Now().UTC())
	})
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
	m.stopPublishers()
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

// unixMilliOrZero 把时间转为毫秒时间戳；零值时间（逻辑点位默认值等无时间来源）转为 0。
func unixMilliOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// normalizeMessage converts one fan-out payload to the wire contract.
func normalizeMessage(message FanoutMessage) natsclient.NormalizedData {
	properties := make(map[string]natsclient.NormalizedProperty, len(message.Properties))
	for _, property := range message.Properties {
		properties[property.Key] = natsclient.NormalizedProperty{
			ID: property.ID, Name: property.Name, Unit: property.Unit, Value: property.Value,
			Timestamp: unixMilliOrZero(property.Timestamp), Quality: property.Quality,
		}
	}
	events := make(map[string]natsclient.NormalizedEvent, len(message.Events))
	for _, event := range message.Events {
		events[event.Key] = natsclient.NormalizedEvent{
			ID: event.ID, Name: event.Name, Level: event.Level, Status: event.Status, Active: event.Active,
			Value: event.Value, Point: event.Point, PointName: event.PointName,
			Timestamp: unixMilliOrZero(event.Timestamp),
		}
	}
	return natsclient.NormalizedData{
		DeviceID: message.DeviceID, DeviceName: message.DeviceName, TemplateCode: message.TemplateCode,
		TemplateVersion: message.TemplateVersion, DataStatus: message.DataStatus, ConfigRevision: message.ConfigRevision,
		Properties: properties, Events: events, Seq: message.Seq, Timestamp: unixMilliOrZero(message.Timestamp),
	}
}

// ─── 周期发布调度（契约 §3：纯周期发布，每设备独立节拍）───

// devicePublisher 一台设备的独立发布节拍（goroutine 生命周期随 stop 关闭）。
type devicePublisher struct {
	stop chan struct{}
	done chan struct{}
}

// syncPublishSchedule 把调度器的设备集与当前配置同步：
// 周期变化（含启停）时全体重启并按索引相位错开首拍；同周期下增量设备立即发首拍，
// 移除的设备停止节拍。各设备周期严格等于全局周期 P，互不排队。
func (m *Manager) syncPublishSchedule() {
	m.mu.RLock()
	interval := time.Duration(m.settings.Publisher.PublishInterval) * time.Millisecond
	m.mu.RUnlock()
	ids := m.registry.DeviceIDs()
	m.publishMu.Lock()
	defer m.publishMu.Unlock()
	restarted := interval != m.publishInterval
	if restarted || interval <= 0 {
		m.stopPublishersLocked()
		m.publishInterval = interval
	}
	if interval <= 0 {
		return
	}
	active := make(map[string]struct{}, len(ids))
	for index, id := range ids {
		active[id] = struct{}{}
		if _, running := m.publishers[id]; running {
			continue
		}
		delay := time.Duration(0)
		if restarted {
			// 全体重启：首拍按索引均匀散布在 [0, P) 内，避免同一瞬间消息洪峰
			delay = time.Duration(int64(interval) * int64(index) / int64(len(ids)))
		}
		m.publishers[id] = m.startDevicePublisher(id, delay, interval)
	}
	for id, publisher := range m.publishers {
		if _, keep := active[id]; !keep {
			close(publisher.stop)
			delete(m.publishers, id)
		}
	}
}

// startDevicePublisher 启动一台设备的独立节拍：延迟 delay 发首拍（新增设备 delay=0 立即可见），
// 之后每 interval 发布一次。每次发布同时执行沿检测并发布告警沿消息。
func (m *Manager) startDevicePublisher(deviceID string, delay, interval time.Duration) *devicePublisher {
	publisher := &devicePublisher{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(publisher.done)
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-publisher.stop:
			return
		case <-timer.C:
			m.publishDevice(deviceID)
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-publisher.stop:
				return
			case <-ticker.C:
				m.publishDevice(deviceID)
			}
		}
	}()
	return publisher
}

func (m *Manager) stopPublishersLocked() {
	for id, publisher := range m.publishers {
		close(publisher.stop)
		delete(m.publishers, id)
	}
}

// stopPublishers 停止全部发布节拍（进程退出时调用）。
func (m *Manager) stopPublishers() {
	m.publishMu.Lock()
	defer m.publishMu.Unlock()
	m.stopPublishersLocked()
}

// publishDevice 执行一台设备的一次北向发布：全量快照 + 告警沿消息。
func (m *Manager) publishDevice(deviceID string) {
	m.mu.RLock()
	north := m.north
	m.mu.RUnlock()
	if north == nil {
		return
	}
	snapshot, ok := m.registry.PublishSnapshot(deviceID, time.Now().UTC())
	if !ok {
		return
	}
	north.PublishData(normalizeMessage(snapshot.Data))
	for _, edge := range snapshot.Edges {
		north.PublishAlarm(edgeAlarmMessage(edge))
	}
}

// edgeAlarmMessage 设备告警沿 → 标准化告警消息（契约 §4.1）。
func edgeAlarmMessage(edge AlarmEdge) natsclient.AlarmMessage {
	return natsclient.AlarmMessage{
		Category: natsclient.AlarmCategoryDevice,
		Kind:     edge.Kind,
		Level:    edge.Event.Level,
		Device: &natsclient.AlarmDevice{
			DeviceID: edge.DeviceID, DeviceName: edge.DeviceName,
			EventID: edge.Event.ID, EventKey: edge.Event.Key, EventName: edge.Event.Name,
			Point: edge.Event.Binding.PropertyKey, PointName: edge.PointName, Value: edge.Value,
			Rule: natsclient.AlarmRule{Type: edge.Event.Type, Threshold: edge.Event.Threshold, Time: edge.Event.Time},
		},
	}
}

// ─── 模型定义变更提示（契约 §4.3）───

// emitModelChanges 对比上次 Apply 的设备集发布 model.changed 提示事件
// （created/updated/deleted/disabled）。首次 Apply 为启动基线，不发布。
func (m *Manager) emitModelChanges(configs []store.DeviceConfig) {
	m.appliedMu.Lock()
	firstLoad := m.applied == nil
	next := make(map[string]store.DeviceConfig, len(configs))
	for _, config := range configs {
		next[config.ID] = config
	}
	previous := m.applied
	m.applied = next
	m.appliedMu.Unlock()
	if firstLoad {
		return
	}
	type change struct {
		deviceID string
		action   string
	}
	var changes []change
	for id, config := range next {
		before, existed := previous[id]
		if !existed {
			changes = append(changes, change{id, "created"})
			continue
		}
		switch {
		case before.Enabled && !config.Enabled:
			changes = append(changes, change{id, "disabled"})
		case !reflect.DeepEqual(before, config):
			changes = append(changes, change{id, "updated"})
		}
	}
	for id := range previous {
		if _, kept := next[id]; !kept {
			changes = append(changes, change{id, "deleted"})
		}
	}
	for _, item := range changes {
		m.publishModelChanged(item.deviceID, item.action)
	}
}

// emitModelRemoved 单设备删除（HTTP DeleteDevice 路径）：同步基准并发布 deleted 事件。
func (m *Manager) emitModelRemoved(id string) {
	m.appliedMu.Lock()
	_, existed := m.applied[id]
	if existed {
		delete(m.applied, id)
	}
	m.appliedMu.Unlock()
	if existed {
		m.publishModelChanged(id, "deleted")
	}
}

// publishModelChanged 发布模型定义变更提示（level=0 提示性事件）。
func (m *Manager) publishModelChanged(deviceID, action string) {
	m.mu.RLock()
	north := m.north
	m.mu.RUnlock()
	if north == nil {
		return
	}
	north.PublishAlarm(natsclient.AlarmMessage{
		Category: natsclient.AlarmCategorySoftware,
		Kind:     natsclient.AlarmKindEvent,
		Level:    0,
		Software: &natsclient.AlarmSoftware{
			Code:      natsclient.AlarmCodeModelChanged,
			Component: "runtime",
			Message:   "物模型设备定义已变更: " + action,
			Context:   map[string]any{"device_id": deviceID, "action": action},
		},
	})
	m.log.Info("模型定义变更事件已发布", "device", deviceID, "action", action)
}

// ─── natsclient.NorthService 实现：查询与控制（与 HTTP 入口共享管道）───

// Devices 设备清单查询（契约 §5.1）。
func (m *Manager) Devices() natsclient.DevicesQueryResponse {
	return natsclient.DevicesQueryResponse{OK: true, Devices: m.registry.DeviceSummaries()}
}

// Properties 属性定义查询（契约 §5.2）。
func (m *Manager) Properties(deviceID string) natsclient.PropertiesQueryResponse {
	if deviceID == "" {
		return natsclient.PropertiesQueryResponse{OK: false, Message: "device_id 不能为空"}
	}
	definitions, ok := m.registry.PropertyDefinitions(deviceID)
	if !ok {
		return natsclient.PropertiesQueryResponse{OK: false, Message: "设备不存在或未启用", DeviceID: deviceID}
	}
	return natsclient.PropertiesQueryResponse{OK: true, DeviceID: deviceID, Properties: definitions}
}

// Methods 服务定义查询（契约 §5.3）。
func (m *Manager) Methods(deviceID string) natsclient.MethodsQueryResponse {
	if deviceID == "" {
		return natsclient.MethodsQueryResponse{OK: false, Message: "device_id 不能为空"}
	}
	definitions, ok := m.registry.MethodDefinitions(deviceID)
	if !ok {
		return natsclient.MethodsQueryResponse{OK: false, Message: "设备不存在或未启用", DeviceID: deviceID}
	}
	return natsclient.MethodsQueryResponse{OK: true, DeviceID: deviceID, Methods: definitions}
}

// Events 告警定义查询（契约 §5.4）。
func (m *Manager) Events(deviceID string) natsclient.EventsQueryResponse {
	if deviceID == "" {
		return natsclient.EventsQueryResponse{OK: false, Message: "device_id 不能为空"}
	}
	definitions, ok := m.registry.EventDefinitions(deviceID)
	if !ok {
		return natsclient.EventsQueryResponse{OK: false, Message: "设备不存在或未启用", DeviceID: deviceID}
	}
	return natsclient.EventsQueryResponse{OK: true, DeviceID: deviceID, Events: definitions}
}

// Control 北向服务控制调用（契约 §6）：REP 只表达受理，终态仅失败经告警主题异步发布。
func (m *Manager) Control(ctx context.Context, request natsclient.ControlRequest, requestID string) natsclient.ControlAck {
	ack := natsclient.ControlAck{RequestID: requestID, DeviceID: request.DeviceID, ServiceKey: request.ServiceKey}
	if request.DeviceID == "" || request.ServiceKey == "" {
		ack.Status = natsclient.AckStatusFailure
		ack.Message = "device_id 与 service_key 不能为空"
		return ack
	}
	record, err := m.InvokeMethod(ctx, request.DeviceID, request.ServiceKey, request.Value)
	if err != nil {
		ack.Status = natsclient.AckStatusFailure
		ack.Message = err.Error()
		return ack
	}
	if record.AcceptedStatus == natsclient.AckStatusFailure {
		ack.Status = natsclient.AckStatusFailure
		ack.Message = record.AcceptedMessage
		return ack
	}
	ack.Status = natsclient.AckStatusAccepted
	return ack
}
