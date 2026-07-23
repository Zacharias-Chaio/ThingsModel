package store

import (
	"path/filepath"
	"testing"
)

func TestDeviceConfigRoundTrip(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "thingsmodel.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("DB() error = %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	config := DeviceConfig{
		ID:           "PCS-A01",
		Name:         "A区1号 PCS",
		TemplateCode: "PCS-DEVICE-001",
		Enabled:      true,
		Properties: []Property{{
			Key:     "voltage",
			Binding: Binding{Method: "EPT", Sources: []BindingSource{{DeviceID: "raw-a01", PropertyID: "voltage"}}},
		}},
	}
	record, err := NewDeviceRecord(config)
	if err != nil {
		t.Fatalf("NewDeviceRecord() error = %v", err)
	}
	if err := db.Create(&record).Error; err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	var stored Device
	if err := db.First(&stored, "id = ?", config.ID).Error; err != nil {
		t.Fatalf("First() error = %v", err)
	}
	actual, err := stored.Config()
	if err != nil {
		t.Fatalf("Config() error = %v", err)
	}
	if actual.ID != config.ID || actual.Properties[0].Binding.Sources[0].PropertyID != "voltage" {
		t.Fatalf("round trip config = %#v, want %s with voltage binding", actual, config.ID)
	}
}
