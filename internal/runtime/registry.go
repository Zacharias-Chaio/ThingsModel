package runtime

import (
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

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
	config       store.DeviceConfig
	revision     int64
	updated      time.Time
	properties   map[string]PropertyStatus
	events       map[string]EventStatus
	eventStarted map[string]time.Time
	logical      map[string]logicalValue // key = property.Key，逻辑点位的缓存值
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
	Key       string    `json:"key"`
	Name      string    `json:"name"`
	Level     int       `json:"level"`
	Status    string    `json:"status"`
	Active    bool      `json:"active"`
	Timestamp time.Time `json:"timestamp"`
	Value     any       `json:"value"`     // 检测属性点位的归一化值（无数据为 null）
	Point     string    `json:"point"`     // 检测属性点位 key
	PointName string    `json:"pointName"` // 检测属性点位名称
	Rules     []string  `json:"rules"`     // 关联告警规则简要（名称+条件+持续时间）
	Method    string    `json:"method"`    // ept | and | or
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
		device := entry{config: config, revision: revision, updated: now, properties: make(map[string]PropertyStatus), events: make(map[string]EventStatus), eventStarted: make(map[string]time.Time)}
		if exists {
			device.properties = current.properties
			device.events = current.events
			device.eventStarted = current.eventStarted
		}
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

// WriteLogical 写入一个逻辑点位：刷新缓存 → 归一化 → 返回需北向发布的 FanoutMessage。
// 仅允许写入 Mode=logical 的属性；设备停用时返回空切片（不发布）。
func (r *Registry) WriteLogical(deviceID, propertyKey string, value any, source string) ([]FanoutMessage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	device, ok := r.devices[deviceID]
	if !ok {
		return nil, fmt.Errorf("设备 %s 不存在", deviceID)
	}
	var target *store.Property
	for i := range device.config.Properties {
		if device.config.Properties[i].Key == propertyKey {
			target = &device.config.Properties[i]
			break
		}
	}
	if target == nil {
		return nil, fmt.Errorf("属性点位 %s 不存在", propertyKey)
	}
	if target.Mode != store.PropertyModeLogical {
		return nil, fmt.Errorf("属性点位 %s 不是逻辑点位", propertyKey)
	}
	now := time.Now().UTC()
	if device.logical == nil {
		device.logical = make(map[string]logicalValue)
	}
	device.logical[propertyKey] = logicalValue{Value: value, Timestamp: now, Source: source}
	r.refreshDevice(&device, now, true)
	r.devices[deviceID] = device
	if !device.config.Enabled {
		return nil, nil
	}
	return []FanoutMessage{fanoutFromEntry(device, now)}, nil
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

// Ingest updates one source device, recalculates affected model devices, and returns fan-out payloads.
func (r *Registry) Ingest(telemetry SourceTelemetry) []FanoutMessage {
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

	affected := r.bindings[sourceID]
	output := make([]FanoutMessage, 0, len(affected))
	for id := range affected {
		device, ok := r.devices[id]
		if !ok {
			continue
		}
		r.refreshDevice(&device, telemetry.ReceivedAt, true)
		r.devices[id] = device
		if device.config.Enabled {
			output = append(output, fanoutFromEntry(device, telemetry.ReceivedAt))
		}
	}
	return output
}

// SourceID is the current upstream identity. Gateway data v1 identifies a source by its gateway and placement.
func SourceID(gatewayID string, channelIndex, deviceIndex int) string {
	return fmt.Sprintf("%s/%d/%d", gatewayID, channelIndex, deviceIndex)
}

func statusFromEntry(device entry) DeviceStatus {
	config := device.config
	status := DeviceStatus{
		ID:             config.ID,
		Name:           config.Name,
		Enabled:        config.Enabled,
		DataStatus:     "unavailable",
		ConfigRevision: device.revision,
		UpdatedAt:      device.updated,
		Properties:     make([]PropertyStatus, 0, len(config.Properties)),
		Methods:        make([]MethodStatus, 0, len(config.Methods)),
		Events:         make([]EventStatus, 0, len(config.Events.Binding)),
	}
	propertyMap := make(map[string]store.Property, len(config.Properties))
	available := false
	degraded := false
	for _, property := range config.Properties {
		propertyMap[property.Key] = property
		propertyStatus, ok := device.properties[property.Key]
		if !ok {
			propertyStatus = PropertyStatus{Key: property.Key, Name: property.Name, Unit: property.Unit, Quality: "unavailable", Status: "unavailable"}
		}
		status.Properties = append(status.Properties, propertyStatus)
		available = available || propertyStatus.Quality == "good"
		degraded = degraded || propertyStatus.Quality == "invalid" || propertyStatus.Quality == "stale" || propertyStatus.Quality == "unmapped"
	}
	for _, method := range config.Methods {
		methodStatus := "unbound"
		targetProperty, targetMode := "", ""
		if method.Binding.PropertyKey != "" {
			methodStatus = "configured"
			targetProperty = method.Binding.PropertyKey
			if target, ok := propertyMap[method.Binding.PropertyKey]; ok {
				targetMode = target.Mode
			}
		}
		status.Methods = append(status.Methods, MethodStatus{
			Key: method.Key, Name: method.Name, Type: method.Type, Status: methodStatus,
			Validation: method.Validation, Descriptions: method.Descriptions,
			TargetProperty: targetProperty, TargetMode: targetMode,
		})
	}
	ruleMap := make(map[string]store.Event, len(config.Events.Rule))
	for _, rule := range config.Events.Rule {
		ruleMap[rule.Key] = rule
	}
	for i := range config.Events.Binding {
		eventStatus, ok := device.events[alarmScope(i)]
		if !ok {
			binding := config.Events.Binding[i]
			key, name, level := alarmMeta(binding, ruleMap)
			pointName := ""
			if target, exists := propertyMap[binding.PropertyKey]; exists {
				pointName = target.Name
			}
			eventStatus = EventStatus{
				Key: key, Name: name, Level: level, Status: "unavailable",
				Point: binding.PropertyKey, PointName: pointName, Rules: ruleBriefs(binding, ruleMap), Method: binding.Method,
			}
		}
		status.Events = append(status.Events, eventStatus)
	}
	if available {
		status.DataStatus = "available"
	} else if degraded {
		status.DataStatus = "degraded"
	}
	return status
}

func fanoutFromEntry(device entry, timestamp time.Time) FanoutMessage {
	status := statusFromEntry(device)
	return FanoutMessage{
		DeviceID: device.config.ID, DeviceName: device.config.Name, TemplateCode: device.config.TemplateCode,
		TemplateVersion: device.config.TemplateVersion, DataStatus: status.DataStatus,
		Properties: status.Properties, Events: status.Events, Timestamp: timestamp,
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
		device.events = make(map[string]EventStatus, len(device.config.Events.Binding))
	}
	if device.eventStarted == nil {
		device.eventStarted = make(map[string]time.Time, len(device.config.Events.Binding))
	}
	if device.logical == nil {
		device.logical = make(map[string]logicalValue)
	}
	for _, property := range device.config.Properties {
		device.properties[property.Key] = r.normalizeProperty(*device, property, now)
	}
	ruleMap := make(map[string]store.Event, len(device.config.Events.Rule))
	for _, rule := range device.config.Events.Rule {
		ruleMap[rule.Key] = rule
	}
	for i := range device.config.Events.Binding {
		scope := alarmScope(i)
		prop := device.properties[device.config.Events.Binding[i].PropertyKey]
		device.events[scope] = r.evaluateAlarm(device.config.Events.Binding[i], ruleMap, prop, device.eventStarted, now, advance, scope)
	}
}

func (r *Registry) normalizeProperty(device entry, property store.Property, now time.Time) PropertyStatus {
	status := PropertyStatus{Key: property.Key, Name: property.Name, Unit: property.Unit, Quality: "unbound", Status: "unavailable"}
	values, timestamp, quality := r.rawValues(device, property, now)
	if quality != "good" && quality != "default" {
		status.Quality = quality
		return status
	}
	status.Timestamp = timestamp
	// 逻辑点位恒为单值直连（ept）；物理点位沿用绑定聚合方法。
	method := property.Binding.Method
	if property.Mode == store.PropertyModeLogical {
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
// logical：读内存缓存（未写入读默认值 / 无值 unavailable）；physical：读上游来源。
func (r *Registry) rawValues(device entry, property store.Property, now time.Time) ([]any, time.Time, string) {
	if property.Mode == store.PropertyModeLogical {
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

// alarmMeta 汇总一条告警关联的展示元信息：key=规则组合，name=规则名组合，level=最高级别。
func alarmMeta(binding store.AlarmBinding, rules map[string]store.Event) (string, string, int) {
	names := make([]string, 0, len(binding.Rules))
	level := 0
	for _, key := range binding.Rules {
		rule, ok := rules[key]
		if !ok {
			continue
		}
		names = append(names, rule.Name)
		if rule.Level > level {
			level = rule.Level
		}
	}
	return strings.Join(binding.Rules, "+"), strings.Join(names, " / "), level
}

// ruleBrief 生成一条告警规则的简要描述：名称 + 比较符 + 阈值 + 持续时间。
func ruleBrief(rule store.Event) string {
	op := rule.Type
	switch rule.Type {
	case "equal":
		op = "="
	case "upper":
		op = ">"
	case "lower":
		op = "<"
	}
	duration := "立即触发"
	if rule.Time > 0 {
		duration = fmt.Sprintf("持续%gs", float64(rule.Time)/1000)
	}
	return fmt.Sprintf("%s %s%v · %s", rule.Name, op, rule.Threshold, duration)
}

// ruleBriefs 生成一条告警关联下所有规则的简要列表（配置中已删除的规则跳过）。
func ruleBriefs(binding store.AlarmBinding, rules map[string]store.Event) []string {
	briefs := make([]string, 0, len(binding.Rules))
	for _, key := range binding.Rules {
		if rule, ok := rules[key]; ok {
			briefs = append(briefs, ruleBrief(rule))
		}
	}
	return briefs
}

// evaluateAlarm 判定一个属性点位的告警状态：先按各规则自身的条件与持续时间
// （rule.Time）逐条判定，再按 method 合并（ept=单规则直判，and=全部成立，or=任一成立）。
// 判定依据为属性点位归一化后的值（与北向发布值一致）：物理点位取聚合值，逻辑点位取缓存/默认值。
// advance=true（遥测/写入路径）更新持续条件计时器；advance=false 只读评估，
// 已触发但未记录开始时间的持续时间告警保持 pending，不会被时钟读取激活。
func (r *Registry) evaluateAlarm(binding store.AlarmBinding, rules map[string]store.Event, prop PropertyStatus, started map[string]time.Time, now time.Time, advance bool, scope string) EventStatus {
	key, name, level := alarmMeta(binding, rules)
	status := EventStatus{
		Key: key, Name: name, Level: level, Status: "unavailable",
		Value: prop.Value, Point: prop.Key, PointName: prop.Name,
		Rules: ruleBriefs(binding, rules), Method: binding.Method,
	}
	// 有值（真实数据 good / 逻辑默认 default）才评估；其余质量清计时器不评估。
	if prop.Quality != "good" && prop.Quality != "default" {
		clearAlarmTimers(started, scope, binding.Rules, advance)
		return status
	}
	status.Timestamp = prop.Timestamp
	number, ok := numberValue(prop.Value)
	if !ok {
		clearAlarmTimers(started, scope, binding.Rules, advance)
		return status
	}
	results := make([]bool, 0, len(binding.Rules))
	pending := false
	for _, ruleKey := range binding.Rules {
		rule, ok := rules[ruleKey]
		if !ok {
			clearAlarmTimers(started, scope, binding.Rules, advance)
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
			clearAlarmTimers(started, scope, binding.Rules, advance)
			return status
		}
		timerKey := scope + "|" + ruleKey
		if !condition {
			if advance {
				delete(started, timerKey)
			}
			results = append(results, false)
			continue
		}
		if started[timerKey].IsZero() {
			if !advance {
				pending = true
				results = append(results, false)
				continue
			}
			started[timerKey] = now
		}
		if rule.Time == 0 || now.Sub(started[timerKey]) >= time.Duration(rule.Time)*time.Millisecond {
			results = append(results, true)
		} else {
			pending = true
			results = append(results, false)
		}
	}
	active := false
	switch binding.Method {
	case "and":
		active = len(results) > 0
		for _, result := range results {
			if !result {
				active = false
			}
		}
	case "or":
		for _, result := range results {
			if result {
				active = true
			}
		}
	default: // ept：单规则直判
		active = len(results) == 1 && results[0]
	}
	if active {
		status.Status = "active"
		status.Active = true
	} else if pending {
		status.Status = "pending"
	} else {
		status.Status = "inactive"
	}
	return status
}

// clearAlarmTimers 清空一条告警关联下所有规则的持续计时器（仅遥测路径 advance=true 时）。
func clearAlarmTimers(started map[string]time.Time, scope string, ruleKeys []string, advance bool) {
	if !advance {
		return
	}
	for _, key := range ruleKeys {
		delete(started, scope+"|"+key)
	}
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
	case "not":
		if len(values) != 1 {
			return nil, false
		}
		if values[0] == 0 {
			return 1, true
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
