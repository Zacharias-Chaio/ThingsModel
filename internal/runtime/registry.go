package runtime

import (
	"fmt"
	"math"
	"reflect"
	"sort"
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
	Key    string `json:"key"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type EventStatus struct {
	Key       string    `json:"key"`
	Name      string    `json:"name"`
	Level     int       `json:"level"`
	Status    string    `json:"status"`
	Active    bool      `json:"active"`
	Timestamp time.Time `json:"timestamp"`
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
				addBinding(nextBindings, source.DeviceID, config.ID)
			}
		}
		for _, event := range config.Events {
			for _, source := range event.Binding {
				addBinding(nextBindings, source.DeviceID, config.ID)
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

func (r *Registry) Get(id string) (DeviceStatus, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	device, ok := r.devices[id]
	if !ok {
		return DeviceStatus{}, false
	}
	r.refreshDevice(&device, time.Now().UTC(), false)
	r.devices[id] = device
	return statusFromEntry(device), true
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

// ResetLiveData clears received source values and active event state while preserving device configuration.
func (r *Registry) ResetLiveData() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sources = make(map[string]sourceEntry)
	for id, device := range r.devices {
		device.properties = make(map[string]PropertyStatus)
		device.events = make(map[string]EventStatus)
		device.eventStarted = make(map[string]time.Time)
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
	if source.Properties == nil {
		source.Properties = make(map[string]SourceProperty, len(telemetry.Properties))
	}
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
		Events:         make([]EventStatus, 0, len(config.Events)),
	}
	available := false
	degraded := false
	for _, property := range config.Properties {
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
		if method.Binding.DeviceID != "" && method.Binding.PropertyID != "" {
			methodStatus = "configured"
		}
		status.Methods = append(status.Methods, MethodStatus{Key: method.Key, Name: method.Name, Status: methodStatus})
	}
	for _, event := range config.Events {
		eventStatus, ok := device.events[event.Key]
		if !ok {
			eventStatus = EventStatus{Key: event.Key, Name: event.Name, Level: event.Level, Status: "unavailable"}
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
		device.events = make(map[string]EventStatus, len(device.config.Events))
	}
	if device.eventStarted == nil {
		device.eventStarted = make(map[string]time.Time, len(device.config.Events))
	}
	for _, property := range device.config.Properties {
		device.properties[property.Key] = r.normalizeProperty(property, now)
	}
	for _, event := range device.config.Events {
		device.events[event.Key] = r.evaluateEvent(event, device.eventStarted, now, advance)
	}
}

func (r *Registry) normalizeProperty(property store.Property, now time.Time) PropertyStatus {
	status := PropertyStatus{Key: property.Key, Name: property.Name, Unit: property.Unit, Quality: "unbound", Status: "unavailable"}
	values, timestamp, quality := r.sourceValues(property.Binding.Sources, now)
	if property.Binding.Method == "" || len(property.Binding.Sources) == 0 {
		return status
	}
	if quality != "good" {
		status.Quality = quality
		return status
	}
	status.Timestamp = timestamp
	if property.Type == "enum" {
		if property.Binding.Method != "EPT" || len(values) != 1 {
			status.Quality = "invalid"
			return status
		}
		for _, description := range property.Description {
			if valuesEqual(description.Value, values[0]) {
				status.Value = description.Enum
				status.Quality = "good"
				status.Status = "available"
				return status
			}
		}
		for _, description := range property.Description {
			if description.Enum == -1 {
				status.Value = -1
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
	value, ok := aggregate(property.Binding.Method, numbers)
	if !ok {
		status.Quality = "invalid"
		return status
	}
	status.Value = value
	status.Quality = "good"
	status.Status = "available"
	return status
}

func (r *Registry) sourceValues(sources []store.BindingSource, now time.Time) ([]any, time.Time, string) {
	values := make([]any, 0, len(sources))
	var timestamp time.Time
	for _, binding := range sources {
		source, ok := r.sources[binding.DeviceID]
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

// evaluateEvent determines the event status from current source values.
// advance=true (telemetry path) updates the sustained-condition timer in
// started; advance=false evaluates read-only against the existing timer, so a
// triggered duration event without a recorded start stays "pending" instead of
// being activated by a wall-clock read.
func (r *Registry) evaluateEvent(event store.Event, started map[string]time.Time, now time.Time, advance bool) EventStatus {
	status := EventStatus{Key: event.Key, Name: event.Name, Level: event.Level, Status: "unavailable"}
	if len(event.Binding) == 0 {
		return status
	}
	values, timestamp, quality := r.sourceValues(event.Binding, now)
	if quality != "good" {
		if advance {
			delete(started, event.Key)
		}
		return status
	}
	status.Timestamp = timestamp
	triggered := false
	for _, value := range values {
		number, ok := numberValue(value)
		if !ok {
			if advance {
				delete(started, event.Key)
			}
			return status
		}
		switch event.Type {
		case "equal":
			triggered = triggered || number == event.Threshold
		case "upper":
			triggered = triggered || number > event.Threshold
		case "lower":
			triggered = triggered || number < event.Threshold
		default:
			if advance {
				delete(started, event.Key)
			}
			return status
		}
	}
	if !triggered {
		if advance {
			delete(started, event.Key)
		}
		status.Status = "inactive"
		return status
	}
	if started[event.Key].IsZero() {
		if !advance {
			status.Status = "pending"
			return status
		}
		started[event.Key] = now
	}
	if event.Time == 0 || now.Sub(started[event.Key]) >= time.Duration(event.Time)*time.Millisecond {
		status.Status = "active"
		status.Active = true
		return status
	}
	status.Status = "pending"
	return status
}

func aggregate(method string, values []float64) (any, bool) {
	if len(values) == 0 {
		return nil, false
	}
	switch method {
	case "EPT":
		return values[0], len(values) == 1
	case "SUM", "AVG":
		total := 0.0
		for _, value := range values {
			total += value
		}
		if method == "AVG" {
			total /= float64(len(values))
		}
		return total, true
	case "MIN":
		minimum := values[0]
		for _, value := range values[1:] {
			minimum = math.Min(minimum, value)
		}
		return minimum, true
	case "MAX":
		maximum := values[0]
		for _, value := range values[1:] {
			maximum = math.Max(maximum, value)
		}
		return maximum, true
	case "AND":
		for _, value := range values {
			if value == 0 {
				return 0, true
			}
		}
		return 1, true
	case "OR":
		for _, value := range values {
			if value != 0 {
				return 1, true
			}
		}
		return 0, true
	case "NOT":
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
