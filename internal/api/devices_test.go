package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"thingsmodel/internal/api"
	"thingsmodel/internal/runtime"
	"thingsmodel/internal/store"
)

func TestSaveDeviceLoadsRuntimeConfiguration(t *testing.T) {
	server := testServer(t)
	config := store.DeviceConfig{
		ID: "PCS-A01", Name: "A区1号 PCS", TemplateCode: "PCS-DEVICE-001", Enabled: true,
		Properties: []store.Property{{
			Key: "voltage", Type: "number",
			Binding: store.Binding{Method: "EPT", Sources: []store.BindingSource{{DeviceID: "raw-a01", PropertyID: "voltage"}}},
		}},
	}
	body, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	server.SaveDevice(response, httptest.NewRequest(http.MethodPost, "/api/devices", bytes.NewReader(body)))
	if response.Code != http.StatusOK {
		t.Fatalf("SaveDevice status = %d, body = %s", response.Code, response.Body.String())
	}
	runtimeResponse := httptest.NewRecorder()
	server.ListRuntimeDevices(runtimeResponse, httptest.NewRequest(http.MethodGet, "/api/runtime/devices", nil))
	if runtimeResponse.Code != http.StatusOK || !bytes.Contains(runtimeResponse.Body.Bytes(), []byte("PCS-A01")) {
		t.Fatalf("runtime response = %d %s", runtimeResponse.Code, runtimeResponse.Body.String())
	}
}

func TestSaveDeviceRejectsEnumAggregation(t *testing.T) {
	server := testServer(t)
	config := store.DeviceConfig{
		ID: "PCS-A02", Name: "A区2号 PCS", TemplateCode: "PCS-DEVICE-001", Enabled: true,
		Properties: []store.Property{{
			Key: "online", Type: "enum",
			Binding: store.Binding{Method: "SUM", Sources: []store.BindingSource{{DeviceID: "raw-a02", PropertyID: "online"}}},
		}},
	}
	body, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	server.SaveDevice(response, httptest.NewRequest(http.MethodPost, "/api/devices", bytes.NewReader(body)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("SaveDevice status = %d, body = %s", response.Code, response.Body.String())
	}
}

func testServer(t *testing.T) *api.Server {
	t.Helper()
	templates := store.NewTemplateStore(t.TempDir())
	if err := templates.Save(&store.Template{
		Code: "PCS-DEVICE-001", Name: "PCS", Version: "1.0.0",
		Properties: []store.Property{
			{Key: "voltage", Name: "电压", Type: "number"},
			{Key: "online", Name: "通讯状态", Type: "enum"},
		},
	}); err != nil {
		t.Fatalf("Save template error = %v", err)
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "thingsmodel.db"))
	if err != nil {
		t.Fatalf("Open database error = %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("Get database connection error = %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return &api.Server{Templates: templates, DB: db, Runtime: runtime.NewRegistry()}
}
