package store

import (
	"encoding/json"
	"fmt"
	"time"
)

// Device is the persistent index for one configured device instance.
// ConfigJSON preserves the full template-compatible instance definition.
type Device struct {
	ID              string    `gorm:"primaryKey;size:128" json:"id"`
	Name            string    `gorm:"not null" json:"name"`
	Description     string    `json:"description"`
	TemplateCode    string    `gorm:"not null;index" json:"templateCode"`
	TemplateVersion string    `json:"templateVersion"`
	Enabled         bool      `gorm:"not null;default:true" json:"enabled"`
	ConfigJSON      []byte    `gorm:"not null" json:"-"`
	CreatedAt       time.Time `json:"-"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

// DeviceConfig is a device-specific snapshot of a template with actual bindings.
// Its properties, methods, and events use the same shape as Template.
type DeviceConfig struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	TemplateCode    string     `json:"templateCode"`
	TemplateVersion string     `json:"templateVersion"`
	Description     string     `json:"description"`
	Enabled         bool       `json:"enabled"`
	Properties      []Property `json:"properties"`
	Methods         []Method   `json:"methods"`
	Events          []Event    `json:"events"`
}

// NewDeviceRecord serializes a device configuration for database storage.
func NewDeviceRecord(config DeviceConfig) (Device, error) {
	raw, err := json.Marshal(config)
	if err != nil {
		return Device{}, fmt.Errorf("序列化设备配置失败: %w", err)
	}
	return Device{
		ID:              config.ID,
		Name:            config.Name,
		Description:     config.Description,
		TemplateCode:    config.TemplateCode,
		TemplateVersion: config.TemplateVersion,
		Enabled:         config.Enabled,
		ConfigJSON:      raw,
	}, nil
}

// Config decodes the complete instance definition stored with the device.
func (d Device) Config() (DeviceConfig, error) {
	var config DeviceConfig
	if err := json.Unmarshal(d.ConfigJSON, &config); err != nil {
		return DeviceConfig{}, fmt.Errorf("解析设备配置失败: %w", err)
	}
	return config, nil
}
