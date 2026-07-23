package api

import (
	"encoding/json"
	"net/http"

	"thingsmodel/internal/runtime"
	"thingsmodel/internal/store"

	"gorm.io/gorm"
)

// Server REST 处理器容器，依赖注入 TemplateStore。
type Server struct {
	Templates *store.TemplateStore
	DB        *gorm.DB
	Runtime   RuntimeFacade
}

// RuntimeFacade keeps API handlers independent from a future collection engine.
type RuntimeFacade interface {
	Apply([]store.DeviceConfig)
	Remove(id string)
	Snapshot() []runtime.DeviceStatus
	Get(id string) (runtime.DeviceStatus, bool)
}

// ok 统一成功响应：{"code":0,"data":<any>}
func ok(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
}

// fail 统一失败响应：{"code":<http>,"msg":"..."}
func fail(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "msg": msg})
}
