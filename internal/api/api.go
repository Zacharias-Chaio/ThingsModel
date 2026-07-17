package api

import (
	"encoding/json"
	"net/http"

	"thingsmodel/internal/store"
)

// Server REST 处理器容器，依赖注入 TemplateStore。
type Server struct {
	Templates *store.TemplateStore
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
