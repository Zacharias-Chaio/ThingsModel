package runtime

import (
	"fmt"
	"math"
	"reflect"
	"sort"
	"sync"
	"time"

	"thingsmodel/internal/natsclient"
	"thingsmodel/internal/store"
)

// Registry holds the currently applied device configurations. It is the hot
// reload boundary that a future collector can replace or extend.
type Registry struct {
	mu         sync.RWMutex
	devices    map[string]entry
	sources    map[string]sourceEntry
	bindings   map[string]map[string]struct{}
	staleAfter time.Duration
}

type entry struct {
	config        store.DeviceConfig
	revision      int64
	updated       time.Time
	properties    map[string]PropertyStatus
	events        map[string]EventStatus
	eventStarted  map[string]time.Time
	logical       map[string]logicalValue // key = property.Key，逻辑点位的缓存值
	seq           uint64                  // 北向发布序列号（单调递增，跳号=下游丢消息）
	lastPublished map[string]string       // scope → 上次发布的告警状态（沿检测基准）
}

// logicalValue 逻辑点位的一次写入：值、时间与来源（service=服务下发，external=外部写入，default 不落缓存）。
type logicalValue struct {
	Value     any
	Timestamp time.Time
	Source    string
}

type sourceEntry struct {
	GatewayID    string
	GatewaySN    string
	ChannelIndex int
	DeviceIndex  int
	DeviceName   string
	CommNo       int
	ModelID      string
	ModelName    string
	LastSeen     time.Time
	Properties   map[string]SourceProperty
}

// DeviceStatus is the runtime view exposed before a data collector is present.
type DeviceStatus struct {
	ID             string           `json:"id"`
	Name           string           `json:"name"`
	Enabled        bool             `json:"enabled"`
	DataStatus     string           `json:"dataStatus"`
	ConfigRevision int64            `json:"configRevision"`
	UpdatedAt      time.Time        `json:"updatedAt"`
	Properties     []PropertyStatus `json:"properties"`
	Methods        []MethodStatus   `json:"methods"`
	Events         []EventStatus    `json:"events"`
}

type PropertyStatus struct {
	ID        string    `json:"id,omitempty"` // 实例条目 ID（property_N）
	Key       string    `json:"key"`
	Name      string    `json:"name"`
	Unit      string    `json:"unit"`
	Value     any       `json:"value"`
	Timestamp time.Time `json:"timestamp"`
	Quality   string    `json:"quality"`
	Status    string    `json:"status"`
}

type MethodStatus struct {
	Key            string             `json:"key"`
	Name           string             `json:"name"`
	Type           string             `json:"type"`                   // status | number
	Status         string             `json:"status"`                 // unbound | configured
	Validation     store.Validation   `json:"validation"`             // number：工程值下发范围（min/max）
	Descriptions   []store.StatusDesc `json:"descriptions,omitempty"` // status：状态值（名称+工程值）
	TargetProperty string             `json:"targetProperty"`         // 目标属性点位 key（未配置为空）
	TargetMode     string             `json:"targetMode"`             // physical | logical
}

type EventStatus struct {
	ID        string    `json:"id,omitempty"` // 实例条目 ID（event_N）
	Key       string    `json:"key"`
	Name      string    `json:"name"`
	Level     int       `json:"level"`
	Status    string    `json:"status"`
	Active    bool      `json:"active"`
	Timestamp time.Time `json:"timestamp"`
	Value     any       `json:"value"`     // 检测属性点位的归一化值（无数据为 null）
	Point     string    `json:"point"`     // 检测属性点位 key
	PointName string    `json:"pointName"` // 检测属性点位名称
}

// SourceDevice is a source discovered from gateway telemetry and available to bind.
type SourceDevice struct {
	ID           string           `json:"id"`
	GatewayID    string           `json:"gatewayId"`
	GatewaySN    string           `json:"gatewaySn"`
	ChannelIndex int              `json:"channelIndex"`
	DeviceIndex  int              `json:"deviceIndex"`
	DeviceName   string           `json:"deviceName"`
	CommNo       int              `json:"commNo"`
	ModelID      string           `json:"modelId"`
	ModelName    string           `json:"modelName"`
	LastSeen     time.Time        `json:"lastSeen"`
	Properties   []SourceProperty `json:"properties"`
}

// SourceProperty mirrors one property from the gateway's MessageData contract.
type SourceProperty struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Unit        string    `json:"unit"`
	Description string    `json:"description"`
	AccessMode  string    `json:"accessMode"`
	Value       any       `json:"value"`
	Timestamp   time.Time `json:"timestamp"`
}

// SourceTelemetry is one full per-device telemetry refresh from an upstream gateway.
type SourceTelemetry struct {
	GatewayID    string
	GatewaySN    string
	ChannelIndex int
	DeviceIndex  int
	DeviceName   string
	CommNo       int
	ModelID      string
	ModelName    string
	ReceivedAt   time.Time
	Properties   map[string]SourceProperty
}

// FanoutMessage is a full normalized device state ready for publication.
type FanoutMessage struct {
	DeviceID        string
	DeviceName      string
	TemplateCode    string
	TemplateVersion string
	DataStatus      string
	ConfigRevision  int64
	Seq             uint64
	Properties      []PropertyStatus
	Events          []EventStatus
	Timestamp       time.Time
}

func NewRegistry() *Registry {
	return &Registry{
		devices:    make(map[string]entry),
		sources:    make(map[string]sourceEntry),
		bindings:   make(map[string]map[string]struct{}),
		staleAfter: time.Minute,
	}
}

// SetStaleAfter configures when a source property stops being valid for normalization.
func (r *Registry) SetStaleAfter(milliseconds int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.staleAfter = time.Duration(milliseconds) * time.Millisecond
}

// Apply reconciles the active device set without restarting the HTTP server.
func (r *Registry) Apply(configs []store.DeviceConfig) {
	next := make(map[string]entry, len(configs))
	nextBindings := make(map[string]map[string]struct{})
	now := time.Now().UTC()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, config := range configs {
		revision := int64(1)
		current, exists := r.devices[config.ID]
		if exists {
			revision = current.revision
			if !reflect.DeepEqual(current.config, config) {
				revision++
				exists = false
			}
		}
		device := entry{config: config, revision: revision, updated: now, properties: make(map[string]PropertyStatus), events: make(map[string]EventStatus), eventStarted: make(map[string]time.Time), lastPublished: make(map[string]string)}
		if exists {
			device.properties = current.properties
			device.events = current.events
			device.eventStarted = current.eventStarted
			device.lastPublished = current.lastPublished // 配置未变化：延续沿检测基准
		}
		device.seq = current.seq // 延续发布序列号（新设备 current 为零值 → 0）
		r.refreshDevice(&device, now, false)
		next[config.ID] = device
		for _, property := range config.Properties {
			for _, source := range property.Binding.Sources {
				addBinding(nextBindings, SourceID(source.GatewayID, source.ChannelID, source.DeviceID), config.ID)
			}
		}
	}
	r.devices = next
	r.bindings = nextBindings
}

// Remove immediately removes a deleted device from the active configuration.
func (r *Registry) Remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.devices, id)
}

func (r *Registry) Snapshot() []DeviceStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]DeviceStatus, 0, len(r.devices))
	now := time.Now().UTC()
	for id, device := range r.devices {
		r.refreshDevice(&device, now, false)
		r.devices[id] = device
		out = append(out, statusFromEntry(device))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Sources returns the latest source-device catalog built from subscribed telemetry.
func (r *Registry) Sources() []SourceDevice {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]SourceDevice, 0, len(r.sources))
	for _, source := range r.sources {
		properties := make([]SourceProperty, 0, len(source.Properties))
		for id, property := range source.Properties {
			property.ID = id
			properties = append(properties, property)
		}
		sort.Slice(properties, func(i, j int) bool { return properties[i].ID < properties[j].ID })
		out = append(out, SourceDevice{
			ID: SourceID(source.GatewayID, source.ChannelIndex, source.DeviceIndex), GatewayID: source.GatewayID,
			GatewaySN: source.GatewaySN, ChannelIndex: source.ChannelIndex, DeviceIndex: source.DeviceIndex,
			DeviceName: source.DeviceName, CommNo: source.CommNo, ModelID: source.ModelID, ModelName: source.ModelName,
			LastSeen: source.LastSeen, Properties: properties,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// BindingRef 枚举一条已配置的属性物理来源引用，供拓扑校验比对：
// 网关侧 channel_index / device_index 是否仍存在、属性是否仍在点表中。
// 服务与告警已改为引用属性点位，不再直接持有网关四级引用，故此处只枚举属性物理来源。
type BindingRef struct {
	DeviceID     string
	DeviceName   string
	Key          string
	Name         string
	GatewayID    string
	ChannelIndex int
	DeviceIndex  int
	PropertyID   string
}

// BindingRefs 返回当前所有已配置的属性物理来源的扁平列表（只读，含未启用设备）。
func (r *Registry) BindingRefs() []BindingRef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]BindingRef, 0)
	for id, device := range r.devices {
		for _, property := range device.config.Properties {
			for _, source := range property.Binding.Sources {
				if source.GatewayID == "" {
					continue
				}
				out = append(out, BindingRef{DeviceID: id, DeviceName: device.config.Name, Key: property.Key, Name: property.Name,
					GatewayID: source.GatewayID, ChannelIndex: source.ChannelID, DeviceIndex: source.DeviceID, PropertyID: source.PropertyID})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].DeviceID != out[j].DeviceID {
			return out[i].DeviceID < out[j].DeviceID
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// MethodTarget 返回服务及其目标属性点位；服务存在但目标属性缺失时 property 为零值、ok=true（防御）。
func (r *Registry) MethodTarget(deviceID, key string) (store.Method, store.Property, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	device, ok := r.devices[deviceID]
	if !ok {
		return store.Method{}, store.Property{}, false
	}
	for _, method := range device.config.Methods {
		if method.Key != key {
			continue
		}
		for _, property := range device.config.Properties {
			if property.Key == method.Binding.PropertyKey {
				return method, property, true
			}
		}
		return method, store.Property{}, true
	}
	return store.Method{}, store.Property{}, false
}

// PointName 解析一个绑定点位在上游遥测中的属性名称（网关 MessageCmd 以属性名下发）。
// 来源尚未接入或点位不存在时回退为 propertyID 本身。
func (r *Registry) PointName(gatewayID string, channelIndex, deviceIndex int, propertyID string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	source, ok := r.sources[SourceID(gatewayID, channelIndex, deviceIndex)]
	if !ok {
		return propertyID
	}
	if property, ok := source.Properties[propertyID]; ok && property.Name != "" {
		return property.Name
	}
	return propertyID
}

// SourceAccessMode 返回某物理点位的当前 access_mode（r/w/rw，来自上游来源目录）。
// 来源未接入或点位不存在时返回 false，调用方应据此决定是否放行下发。
func (r *Registry) SourceAccessMode(gatewayID string, channelIndex, deviceIndex int, propertyID string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	source, ok := r.sources[SourceID(gatewayID, channelIndex, deviceIndex)]
	if !ok {
		return "", false
	}
	property, ok := source.Properties[propertyID]
	if !ok {
		return "", false
	}
	return property.AccessMode, true
}

// WriteLogical 写入一个逻辑点位并刷新缓存（北向发布由周期调度器统一承载）。
// 仅允许写入 Mode=logical 的属性。
func (r *Registry) WriteLogical(deviceID, propertyKey string, value any, source string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	device, ok := r.devices[deviceID]
	if !ok {
		return fmt.Errorf("设备 %s 不存在", deviceID)
	}
	var target *store.Property
	for i := range device.config.Properties {
		if device.config.Properties[i].Key == propertyKey {
			target = &device.config.Properties[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("属性点位 %s 不存在", propertyKey)
	}
	if target.Mode != store.PropertyModeLogical {
		return fmt.Errorf("属性点位 %s 不是逻辑点位", propertyKey)
	}
	if target.Binding.Method != "" && target.Binding.Method != "ept" {
		return fmt.Errorf("属性点位 %s 为聚合逻辑点位，无写入权限", propertyKey)
	}
	now := time.Now().UTC()
	if device.logical == nil {
		device.logical = make(map[string]logicalValue)
	}
	device.logical[propertyKey] = logicalValue{Value: value, Timestamp: now, Source: source}
	r.refreshDevice(&device, now, true)
	r.devices[deviceID] = device
	return nil
}

// DeviceName 返回设备展示名（供命令记录等用途）。
func (r *Registry) DeviceName(deviceID string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	device, ok := r.devices[deviceID]
	if !ok {
		return "", false
	}
	return device.config.Name, true
}

// PublishedSnapshot 一次北向发布的内容：设备全量快照 + 本拍产生的告警沿。
type PublishedSnapshot struct {
	Data  FanoutMessage
	Edges []AlarmEdge
}

// AlarmEdge 一条告警沿（trigger/recover），由发布边界的状态迁移产生。
type AlarmEdge struct {
	DeviceID    string
	DeviceName  string
	Event       store.DeviceEvent
	PointName   string
	Value       any
	Kind        string // trigger | recover
}

// PublishSnapshot 对一台设备执行一次北向发布：纯读重算（不推进告警计时器）、
// 推进发布序列号、执行沿检测（上次发布状态 → 本次状态）。
// 设备不存在或未启用时返回 false。断流（属性值龄超 staleAfter）时告警
// 评估自然产出 unavailable 状态（B 方案投影），active → unavailable 不产生沿。
func (r *Registry) PublishSnapshot(deviceID string, now time.Time) (PublishedSnapshot, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	device, ok := r.devices[deviceID]
	if !ok || !device.config.Enabled {
		return PublishedSnapshot{}, false
	}
	if device.lastPublished == nil {
		device.lastPublished = make(map[string]string)
	}
	r.refreshDevice(&device, now, false)
	device.seq++
	edges := r.detectEdges(&device)
	snapshot := fanoutFromEntry(device, now)
	snapshot.Seq = device.seq
	r.devices[deviceID] = device
	return PublishedSnapshot{Data: snapshot, Edges: edges}, true
}

// detectEdges 沿检测（契约 §4.1 迁移表）：比较上次发布状态与本次状态，
// 产出 trigger（非 active → active）与 recover（active → inactive）。
// active → unavailable（断流投影）及其他迁移不产生消息。
func (r *Registry) detectEdges(device *entry) []AlarmEdge {
	var edges []AlarmEdge
	for i := range device.config.Events {
		event := device.config.Events[i]
		scope := alarmScope(i)
		current := ""
		if status, ok := device.events[scope]; ok {
			current = status.Status
		}
		previous := device.lastPublished[scope]
		device.lastPublished[scope] = current
		kind := ""
		switch {
		case current == "active" && previous != "active":
			kind = natsclient.AlarmKindTrigger
		case previous == "active" && current == "inactive":
			kind = natsclient.AlarmKindRecover
		}
		if kind == "" {
			continue
		}
		prop := device.properties[event.Binding.PropertyKey]
		edges = append(edges, AlarmEdge{
			DeviceID: device.config.ID, DeviceName: device.config.Name,
			Event: event, PointName: prop.Name, Value: prop.Value, Kind: kind,
		})
	}
	return edges
}

// DeviceIDs 返回已启用设备的 ID 列表（字典序），供周期调度器编排。
func (r *Registry) DeviceIDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.devices))
	for id, device := range r.devices {
		if device.config.Enabled {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// DeviceSummaries 返回设备清单概要（契约 §5.1；未启用设备不对外）。
// 取写锁做纯读重算（与 Snapshot 相同模式），保证 DataStatus 反映当前陈旧状态。
func (r *Registry) DeviceSummaries() []natsclient.DeviceSummary {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]natsclient.DeviceSummary, 0, len(r.devices))
	now := time.Now().UTC()
	for _, device := range r.devices {
		if !device.config.Enabled {
			continue
		}
		snapshot := device
		r.refreshDevice(&snapshot, now, false)
		out = append(out, natsclient.DeviceSummary{
			DeviceID: device.config.ID, DeviceName: device.config.Name,
			TemplateCode: device.config.TemplateCode, TemplateVersion: device.config.TemplateVersion,
			Description: device.config.Description, Enabled: true,
			DataStatus:    dataStatusFromProperties(propertiesFromEntry(snapshot)),
			PropertyCount: len(device.config.Properties), MethodCount: len(device.config.Methods),
			EventCount: len(device.config.Events),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DeviceID < out[j].DeviceID })
	return out
}

// PropertyDefinitions 返回设备属性定义（契约 §5.2）；设备不存在或未启用时 ok=false。
func (r *Registry) PropertyDefinitions(deviceID string) ([]natsclient.PropertyDefinition, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	device, ok := r.devices[deviceID]
	if !ok || !device.config.Enabled {
		return nil, false
	}
	out := make([]natsclient.PropertyDefinition, 0, len(device.config.Properties))
	for _, property := range device.config.Properties {
		definition := natsclient.PropertyDefinition{
			ID: property.ID, Key: property.Key, Name: property.Name, Unit: property.Unit,
			Type: property.Type, Mode: property.Mode, Method: property.Binding.Method, Default: property.Default,
		}
		if definition.Mode == "" {
			definition.Mode = store.PropertyModePhysical
		}
		if definition.Method == "" {
			definition.Method = "ept"
		}
		if property.Type == "status" {
			definition.Descriptions = statusDefinitions(property.Description)
		}
		out = append(out, definition)
	}
	return out, true
}

// MethodDefinitions 返回设备服务定义（契约 §5.3，控制能力清单）；设备不存在或未启用时 ok=false。
func (r *Registry) MethodDefinitions(deviceID string) ([]natsclient.MethodDefinition, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	device, ok := r.devices[deviceID]
	if !ok || !device.config.Enabled {
		return nil, false
	}
	propertyModes := make(map[string]string, len(device.config.Properties))
	for _, property := range device.config.Properties {
		propertyModes[property.Key] = property.Mode
	}
	out := make([]natsclient.MethodDefinition, 0, len(device.config.Methods))
	for _, method := range device.config.Methods {
		definition := natsclient.MethodDefinition{
			ID: method.ID, Key: method.Key, Name: method.Name, Type: method.Type,
			Bound: method.Binding.PropertyKey != "",
		}
		if method.Binding.PropertyKey != "" {
			definition.TargetMode = propertyModes[method.Binding.PropertyKey]
		}
		if method.Type == "number" {
			definition.Validation = &natsclient.RangeValidation{Min: method.Validation.Min, Max: method.Validation.Max}
		}
		if method.Type == "status" {
			definition.Descriptions = statusDefinitions(method.Descriptions)
		}
		out = append(out, definition)
	}
	return out, true
}

// EventDefinitions 返回设备告警定义（契约 §5.4）；设备不存在或未启用时 ok=false。
func (r *Registry) EventDefinitions(deviceID string) ([]natsclient.EventDefinition, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	device, ok := r.devices[deviceID]
	if !ok || !device.config.Enabled {
		return nil, false
	}
	propertyNames := make(map[string]string, len(device.config.Properties))
	for _, property := range device.config.Properties {
		propertyNames[property.Key] = property.Name
	}
	out := make([]natsclient.EventDefinition, 0, len(device.config.Events))
	for _, event := range device.config.Events {
		definition := natsclient.EventDefinition{
			ID: event.ID, Key: event.Key, Name: event.Name, Description: event.Description,
			Level: event.Level, Type: event.Type, Threshold: event.Threshold, Time: event.Time,
			BindingProperty: event.Binding.PropertyKey,
		}
		if event.Binding.PropertyKey != "" {
			definition.BindingPropertyName = propertyNames[event.Binding.PropertyKey]
		}
		out = append(out, definition)
	}
	return out, true
}

// statusDefinitions 模板状态值描述 → 北向契约状态值。
func statusDefinitions(descriptions []store.StatusDesc) []natsclient.StatusDefinition {
	out := make([]natsclient.StatusDefinition, 0, len(descriptions))
	for _, description := range descriptions {
		out = append(out, natsclient.StatusDefinition{Enum: description.Enum, Key: description.Key, Name: description.Name, Value: description.Value})
	}
	return out
}

// ResetLiveData clears received source values and active event state while preserving device configuration.
func (r *Registry) ResetLiveData() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sources = make(map[string]sourceEntry)
	for id, device := range r.devices {
		device.properties = make(map[string]PropertyStatus)
		device.events = make(map[string]EventStatus)
		device.eventStarted = make(map[string]time.Time)
		device.logical = make(map[string]logicalValue)
		r.refreshDevice(&device, time.Now().UTC(), false)
		r.devices[id] = device
	}
}

// Ingest 更新一个来源设备并重算受影响的模型设备（推进告警持续时间计时器）。
// 纯周期发布模型下不产生北向扇出：北向数据由周期调度器统一发布。
func (r *Registry) Ingest(telemetry SourceTelemetry) {
	if telemetry.ReceivedAt.IsZero() {
		telemetry.ReceivedAt = time.Now().UTC()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	sourceID := SourceID(telemetry.GatewayID, telemetry.ChannelIndex, telemetry.DeviceIndex)
	source := r.sources[sourceID]
	source.GatewayID = telemetry.GatewayID
	source.GatewaySN = telemetry.GatewaySN
	source.ChannelIndex = telemetry.ChannelIndex
	source.DeviceIndex = telemetry.DeviceIndex
	source.DeviceName = telemetry.DeviceName
	source.CommNo = telemetry.CommNo
	source.ModelID = telemetry.ModelID
	source.ModelName = telemetry.ModelName
	source.LastSeen = telemetry.ReceivedAt
	// 全量刷新：properties 为该设备当前完整点位集，整体替换；
	// 消息中未包含的点位视为不存在（属性值显示为空，而非残留旧值）。
	source.Properties = make(map[string]SourceProperty, len(telemetry.Properties))
	for id, property := range telemetry.Properties {
		if property.Timestamp.IsZero() {
			property.Timestamp = telemetry.ReceivedAt
		}
		source.Properties[id] = property
	}
	r.sources[sourceID] = source

	for id := range r.bindings[sourceID] {
		device, ok := r.devices[id]
		if !ok {
			continue
		}
		r.refreshDevice(&device, telemetry.ReceivedAt, true)
		r.devices[id] = device
	}
}

// SourceID is the current upstream identity. Gateway data v1 identifies a source by its gateway and placement.
func SourceID(gatewayID string, channelIndex, deviceIndex int) string {
	return fmt.Sprintf("%s/%d/%d", gatewayID, channelIndex, deviceIndex)
}

// propertiesFromEntry 组装属性状态列表（缺失项填 unavailable 默认态）。
func propertiesFromEntry(device entry) []PropertyStatus {
	out := make([]PropertyStatus, 0, len(device.config.Properties))
	for _, property := range device.config.Properties {
		if status, ok := device.properties[property.Key]; ok {
			out = append(out, status)
			continue
		}
		out = append(out, PropertyStatus{ID: property.ID, Key: property.Key, Name: property.Name, Unit: property.Unit, Quality: "unavailable", Status: "unavailable"})
	}
	return out
}

// dataStatusFromProperties 依据属性质量判定设备数据状态：
// 任一 good → available；否则任一 invalid/stale/unmapped → degraded；其余 unavailable。
func dataStatusFromProperties(properties []PropertyStatus) string {
	available, degraded := false, false
	for _, property := range properties {
		switch property.Quality {
		case "good":
			available = true
		case "invalid", "stale", "unmapped":
			degraded = true
		}
	}
	if available {
		return "available"
	}
	if degraded {
		return "degraded"
	}
	return "unavailable"
}

// eventsFromEntry 组装告警状态列表（未评估项按绑定情况填 unavailable/unbound 默认态）。
func eventsFromEntry(device entry) []EventStatus {
	propertyNames := make(map[string]string, len(device.config.Properties))
	for _, property := range device.config.Properties {
		propertyNames[property.Key] = property.Name
	}
	out := make([]EventStatus, 0, len(device.config.Events))
	for i := range device.config.Events {
		event := device.config.Events[i]
		if status, ok := device.events[alarmScope(i)]; ok {
			out = append(out, status)
			continue
		}
		status := "unavailable"
		if event.Binding.PropertyKey == "" {
			status = "unbound"
		}
		out = append(out, EventStatus{
			ID: event.ID, Key: event.Key, Name: event.Name, Level: event.Level, Status: status,
			Point: event.Binding.PropertyKey, PointName: propertyNames[event.Binding.PropertyKey],
		})
	}
	return out
}

// methodsFromEntry 组装服务状态列表（绑定状态 + 目标点位模式）。
func methodsFromEntry(device entry) []MethodStatus {
	propertyModes := make(map[string]string, len(device.config.Properties))
	for _, property := range device.config.Properties {
		propertyModes[property.Key] = property.Mode
	}
	out := make([]MethodStatus, 0, len(device.config.Methods))
	for _, method := range device.config.Methods {
		methodStatus := "unbound"
		targetProperty, targetMode := "", ""
		if method.Binding.PropertyKey != "" {
			methodStatus = "configured"
			targetProperty = method.Binding.PropertyKey
			targetMode = propertyModes[method.Binding.PropertyKey]
		}
		out = append(out, MethodStatus{
			Key: method.Key, Name: method.Name, Type: method.Type, Status: methodStatus,
			Validation: method.Validation, Descriptions: method.Descriptions,
			TargetProperty: targetProperty, TargetMode: targetMode,
		})
	}
	return out
}

// statusFromEntry 组装设备完整状态视图（HTTP 运行时接口用，含服务列表）。
func statusFromEntry(device entry) DeviceStatus {
	properties := propertiesFromEntry(device)
	return DeviceStatus{
		ID:             device.config.ID,
		Name:           device.config.Name,
		Enabled:        device.config.Enabled,
		DataStatus:     dataStatusFromProperties(properties),
		ConfigRevision: device.revision,
		UpdatedAt:      device.updated,
		Properties:     properties,
		Methods:        methodsFromEntry(device),
		Events:         eventsFromEntry(device),
	}
}

// fanoutFromEntry 组装北向发布快照：只取属性与告警状态，不构建服务列表（热路径精简）。
func fanoutFromEntry(device entry, timestamp time.Time) FanoutMessage {
	properties := propertiesFromEntry(device)
	return FanoutMessage{
		DeviceID: device.config.ID, DeviceName: device.config.Name, TemplateCode: device.config.TemplateCode,
		TemplateVersion: device.config.TemplateVersion, DataStatus: dataStatusFromProperties(properties),
		ConfigRevision: device.revision, Properties: properties, Events: eventsFromEntry(device), Timestamp: timestamp,
	}
}

func addBinding(bindings map[string]map[string]struct{}, sourceID, deviceID string) {
	if sourceID == "" {
		return
	}
	if bindings[sourceID] == nil {
		bindings[sourceID] = make(map[string]struct{})
	}
	bindings[sourceID][deviceID] = struct{}{}
}

// refreshDevice recomputes property and event statuses as of now. Only the
// telemetry path passes advance=true; read paths must stay pure so wall-clock
// reads cannot start or expire sustained-condition timers.
func (r *Registry) refreshDevice(device *entry, now time.Time, advance bool) {
	if device.properties == nil {
		device.properties = make(map[string]PropertyStatus, len(device.config.Properties))
	}
	if device.events == nil {
		device.events = make(map[string]EventStatus, len(device.config.Events))
	}
	if device.eventStarted == nil {
		device.eventStarted = make(map[string]time.Time, len(device.config.Events))
	}
	if device.logical == nil {
		device.logical = make(map[string]logicalValue)
	}
	for _, property := range device.config.Properties {
		device.properties[property.Key] = r.normalizeProperty(*device, property, now)
	}
	for i := range device.config.Events {
		scope := alarmScope(i)
		event := device.config.Events[i]
		if event.Binding.PropertyKey == "" {
			delete(device.events, scope) // 未配置监测点位：清除状态、不评估
			continue
		}
		prop := device.properties[event.Binding.PropertyKey]
		device.events[scope] = r.evaluateAlarm(event, prop, device.eventStarted, now, advance, scope)
	}
}

func (r *Registry) normalizeProperty(device entry, property store.Property, now time.Time) PropertyStatus {
	status := PropertyStatus{ID: property.ID, Key: property.Key, Name: property.Name, Unit: property.Unit, Quality: "unbound", Status: "unavailable"}
	values, timestamp, quality := r.rawValues(device, property, now)
	if quality != "good" && quality != "default" {
		status.Quality = quality
		return status
	}
	status.Timestamp = timestamp
	// 方法：physical 恒 ept；logical ept 走缓存值、非 ept 走来源聚合。
	method := property.Binding.Method
	if method == "" || property.Mode == store.PropertyModePhysical {
		method = "ept"
	}
	if property.Type == "status" {
		if method != "ept" || len(values) != 1 {
			status.Quality = "invalid"
			return status
		}
		for _, description := range property.Description {
			if valuesEqual(description.Value, values[0]) {
				status.Value = description.Enum
				status.Quality = quality
				status.Status = "available"
				return status
			}
		}
		for _, description := range property.Description {
			if description.Enum == -999 {
				status.Value = -999
				break
			}
		}
		status.Quality = "unmapped"
		return status
	}

	numbers := make([]float64, 0, len(values))
	for _, value := range values {
		number, ok := numberValue(value)
		if !ok || math.IsNaN(number) || math.IsInf(number, 0) {
			status.Quality = "invalid"
			return status
		}
		numbers = append(numbers, number)
	}
	value, ok := aggregate(method, numbers)
	if !ok {
		status.Quality = "invalid"
		return status
	}
	status.Value = value
	status.Quality = quality
	status.Status = "available"
	return status
}

// rawValues 返回属性点位的原始工程值列表、时间戳与质量。
// physical：读上游来源（单来源 ept）；logical+ept：读内存缓存/默认值；
// logical+非 ept：读绑定的多个上游物理来源（只读聚合）。
func (r *Registry) rawValues(device entry, property store.Property, now time.Time) ([]any, time.Time, string) {
	if property.Mode == store.PropertyModeLogical && (property.Binding.Method == "" || property.Binding.Method == "ept") {
		if v, ok := device.logical[property.Key]; ok {
			return []any{v.Value}, v.Timestamp, "good"
		}
		if property.Default != nil {
			return []any{property.Default}, time.Time{}, "default"
		}
		return nil, time.Time{}, "unavailable"
	}
	if len(property.Binding.Sources) == 0 {
		return nil, time.Time{}, "unbound"
	}
	return r.sourceValues(property.Binding.Sources, now)
}

func (r *Registry) sourceValues(sources []store.BindingSource, now time.Time) ([]any, time.Time, string) {
	values := make([]any, 0, len(sources))
	var timestamp time.Time
	for _, binding := range sources {
		source, ok := r.sources[SourceID(binding.GatewayID, binding.ChannelID, binding.DeviceID)]
		if !ok {
			return nil, time.Time{}, "unavailable"
		}
		property, ok := source.Properties[binding.PropertyID]
		if !ok {
			return nil, time.Time{}, "unavailable"
		}
		if property.Value == nil {
			return nil, time.Time{}, "invalid"
		}
		if r.staleAfter > 0 && now.Sub(property.Timestamp) > r.staleAfter {
			return nil, time.Time{}, "stale"
		}
		if property.Timestamp.After(timestamp) {
			timestamp = property.Timestamp
		}
		values = append(values, property.Value)
	}
	return values, timestamp, "good"
}

// alarmScope 生成告警关联在设备内的稳定标识（用于状态与计时器索引）。
func alarmScope(i int) string {
	return fmt.Sprintf("alarm-%d", i)
}

// evaluateAlarm 判定一条告警条目的状态：单规则按条件 + 持续时间（rule.Time）独立判定。
// 判定依据为属性点位归一化后的值（与北向发布值一致）：物理点位取聚合值，逻辑点位取缓存/默认值。
// advance=true（遥测/写入路径）更新持续条件计时器；advance=false 只读评估，
// 已触发但未记录开始时间的持续时间告警保持 pending，不会被时钟读取激活。
func (r *Registry) evaluateAlarm(event store.DeviceEvent, prop PropertyStatus, started map[string]time.Time, now time.Time, advance bool, scope string) EventStatus {
	rule := event.Event
	status := EventStatus{
		ID: event.ID, Key: rule.Key, Name: rule.Name, Level: rule.Level, Status: "unavailable",
		Value: prop.Value, Point: event.Binding.PropertyKey, PointName: prop.Name,
	}
	// 有值（真实数据 good / 逻辑默认 default）才评估；其余质量清计时器不评估。
	if prop.Quality != "good" && prop.Quality != "default" {
		clearAlarmTimers(started, scope, advance)
		return status
	}
	status.Timestamp = prop.Timestamp
	number, ok := numberValue(prop.Value)
	if !ok {
		clearAlarmTimers(started, scope, advance)
		return status
	}
	condition := false
	switch rule.Type {
	case "equal":
		condition = number == rule.Threshold
	case "upper":
		condition = number > rule.Threshold
	case "lower":
		condition = number < rule.Threshold
	default:
		clearAlarmTimers(started, scope, advance)
		return status
	}
	if !condition {
		if advance {
			delete(started, scope)
		}
		status.Status = "inactive"
		return status
	}
	if started[scope].IsZero() {
		if !advance {
			status.Status = "pending"
			return status
		}
		started[scope] = now
	}
	if rule.Time == 0 || now.Sub(started[scope]) >= time.Duration(rule.Time)*time.Millisecond {
		status.Status = "active"
		status.Active = true
	} else {
		status.Status = "pending"
	}
	return status
}

// clearAlarmTimers 清空一条告警条目的持续计时器（仅遥测路径 advance=true 时）。
func clearAlarmTimers(started map[string]time.Time, scope string, advance bool) {
	if !advance {
		return
	}
	delete(started, scope)
}

func aggregate(method string, values []float64) (any, bool) {
	if len(values) == 0 {
		return nil, false
	}
	switch method {
	case "ept":
		return values[0], len(values) == 1
	case "sum", "avg":
		total := 0.0
		for _, value := range values {
			total += value
		}
		if method == "avg" {
			total /= float64(len(values))
		}
		return total, true
	case "min":
		minimum := values[0]
		for _, value := range values[1:] {
			minimum = math.Min(minimum, value)
		}
		return minimum, true
	case "max":
		maximum := values[0]
		for _, value := range values[1:] {
			maximum = math.Max(maximum, value)
		}
		return maximum, true
	case "and":
		for _, value := range values {
			if value == 0 {
				return 0, true
			}
		}
		return 1, true
	case "or":
		for _, value := range values {
			if value != 0 {
				return 1, true
			}
		}
		return 0, true
	default:
		return nil, false
	}
}

func numberValue(value any) (float64, bool) {
	switch number := value.(type) {
	case float64:
		return number, true
	case float32:
		return float64(number), true
	case int:
		return float64(number), true
	case int8:
		return float64(number), true
	case int16:
		return float64(number), true
	case int32:
		return float64(number), true
	case int64:
		return float64(number), true
	case uint:
		return float64(number), true
	case uint8:
		return float64(number), true
	case uint16:
		return float64(number), true
	case uint32:
		return float64(number), true
	case uint64:
		return float64(number), true
	default:
		return 0, false
	}
}

func valuesEqual(left, right any) bool {
	leftNumber, leftIsNumber := numberValue(left)
	rightNumber, rightIsNumber := numberValue(right)
	if leftIsNumber && rightIsNumber {
		return leftNumber == rightNumber
	}
	return reflect.DeepEqual(left, right)
}
