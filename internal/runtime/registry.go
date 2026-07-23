package runtime

import (
	"sort"
	"sync"
	"time"

	"thingsmodel/internal/store"
)

// Registry holds the currently applied device configurations. It is the hot
// reload boundary that a future collector can replace or extend.
type Registry struct {
	mu      sync.RWMutex
	devices map[string]entry
}

type entry struct {
	config   store.DeviceConfig
	revision int64
	updated  time.Time
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
	Key    string `json:"key"`
	Name   string `json:"name"`
	Unit   string `json:"unit"`
	Status string `json:"status"`
}

type MethodStatus struct {
	Key    string `json:"key"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type EventStatus struct {
	Key    string `json:"key"`
	Name   string `json:"name"`
	Level  int    `json:"level"`
	Status string `json:"status"`
}

func NewRegistry() *Registry {
	return &Registry{devices: make(map[string]entry)}
}

// Apply replaces the active device set without restarting the HTTP server.
func (r *Registry) Apply(configs []store.DeviceConfig) {
	next := make(map[string]entry, len(configs))
	now := time.Now().UTC()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, config := range configs {
		revision := int64(1)
		if current, ok := r.devices[config.ID]; ok {
			revision = current.revision + 1
		}
		next[config.ID] = entry{config: config, revision: revision, updated: now}
	}
	r.devices = next
}

// Remove immediately removes a deleted device from the active configuration.
func (r *Registry) Remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.devices, id)
}

func (r *Registry) Snapshot() []DeviceStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]DeviceStatus, 0, len(r.devices))
	for _, device := range r.devices {
		out = append(out, statusFromEntry(device))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (r *Registry) Get(id string) (DeviceStatus, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	device, ok := r.devices[id]
	if !ok {
		return DeviceStatus{}, false
	}
	return statusFromEntry(device), true
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
	for _, property := range config.Properties {
		status.Properties = append(status.Properties, PropertyStatus{Key: property.Key, Name: property.Name, Unit: property.Unit, Status: "unavailable"})
	}
	for _, method := range config.Methods {
		status.Methods = append(status.Methods, MethodStatus{Key: method.Key, Name: method.Name, Status: "unavailable"})
	}
	for _, event := range config.Events {
		status.Events = append(status.Events, EventStatus{Key: event.Key, Name: event.Name, Level: event.Level, Status: "unavailable"})
	}
	return status
}
