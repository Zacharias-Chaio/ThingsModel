package api

import (
	"encoding/json"
	"net/http"
	"runtime"
	"time"

	"thingsmodel/internal/buildinfo"
	"thingsmodel/internal/config"
	"thingsmodel/internal/logging"
)

// GetSettings returns the editable application settings persisted in the database.
func (s *Server) GetSettings(w http.ResponseWriter, r *http.Request) {
	if s.Settings == nil {
		fail(w, http.StatusServiceUnavailable, "平台设置未就绪")
		return
	}
	ok(w, s.Settings.Get())
}

// SaveSettings persists settings, then hot-reloads logging and the NATS client.
func (s *Server) SaveSettings(w http.ResponseWriter, r *http.Request) {
	if s.Settings == nil {
		fail(w, http.StatusServiceUnavailable, "平台设置未就绪")
		return
	}
	var settings config.App
	if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
		fail(w, http.StatusBadRequest, "JSON 解析失败: "+err.Error())
		return
	}
	if err := s.Settings.Save(settings); err != nil {
		fail(w, http.StatusBadRequest, "保存平台设置失败: "+err.Error())
		return
	}
	if s.Logger != nil {
		if err := s.Logger.Apply(logging.Config{Logger: settings.Logger}); err != nil {
			fail(w, http.StatusInternalServerError, "日志设置已保存但加载失败: "+err.Error())
			return
		}
	}
	if manager, ok := s.Runtime.(RuntimeAdmin); ok {
		if err := manager.ReloadBus(settings); err != nil {
			fail(w, http.StatusServiceUnavailable, "设置已保存，但消息总线热加载失败: "+err.Error())
			return
		}
	}
	ok(w, settings)
}

type SystemInfo struct {
	OperatingSystem    string `json:"operatingSystem"`
	SystemTime         string `json:"systemTime"`
	ThingsModelVersion string `json:"thingsModelVersion"`
}

func (s *Server) GetSystemInfo(w http.ResponseWriter, r *http.Request) {
	ok(w, SystemInfo{
		OperatingSystem:    runtime.GOOS,
		SystemTime:         time.Now().UTC().Format(time.RFC3339),
		ThingsModelVersion: buildinfo.Version,
	})
}

// Restart restarts message-bus resources in process while the HTTP server remains available.
func (s *Server) Restart(w http.ResponseWriter, r *http.Request) {
	if s.Settings == nil {
		fail(w, http.StatusServiceUnavailable, "平台设置未就绪")
		return
	}
	manager, ok := s.Runtime.(RuntimeAdmin)
	if !ok {
		fail(w, http.StatusServiceUnavailable, "运行时重启不可用")
		return
	}
	if !manager.Restart(s.Settings.Get()) {
		fail(w, http.StatusConflict, "软件正在重启，请稍候")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]string{"status": "restarting"}})
}

// ListSources provides the latest upstream device and property catalog for binding UI selection.
func (s *Server) ListSources(w http.ResponseWriter, r *http.Request) {
	sources, supported := s.Runtime.(SourceFacade)
	if !supported {
		ok(w, []any{})
		return
	}
	ok(w, sources.Sources())
}
