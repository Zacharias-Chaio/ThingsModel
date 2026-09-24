// commands.go - 控制下发 API：Web 页面 → 南向 .cmd REQ/REP → cmdAck 终态查询。
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"thingsmodel/internal/runtime"

	"github.com/go-chi/chi/v5"
)

// CommandInvoker 暴露控制下发能力，保持 handler 与 Manager 实现解耦。
type CommandInvoker interface {
	InvokeMethod(ctx context.Context, deviceID, methodKey string, value float64) (runtime.CommandRecord, error)
	Command(requestID string) (runtime.CommandRecord, bool)
}

// invokeTimeout 单次下发（REQ/REP 同步等待 accepted）的预算。
const invokeTimeout = 8 * time.Second

// InvokeDeviceMethod 处理 POST /api/runtime/devices/{id}/methods/{key}，body: {"value": <工程值>}。
// 返回受理结果：acceptedStatus=accepted 表示网关已受理并投递引擎写队列，
// 最终执行结果由网关复用 .data 主题异步回报 cmdAck，前端凭 requestId 轮询 GetCommand。
func (s *Server) InvokeDeviceMethod(w http.ResponseWriter, r *http.Request) {
	invoker, supported := s.Runtime.(CommandInvoker)
	if !supported {
		fail(w, http.StatusServiceUnavailable, "控制下发不可用")
		return
	}
	var body struct {
		Value *float64 `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Value == nil {
		fail(w, http.StatusBadRequest, "请求体需为 {\"value\": <数字工程值>}")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), invokeTimeout)
	defer cancel()
	record, err := invoker.InvokeMethod(ctx, chi.URLParam(r, "id"), chi.URLParam(r, "key"), *body.Value)
	if err != nil {
		// 网关拒绝或链路失败：record 已落命令记录，仍以错误返回并携带受理详情。
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	ok(w, record)
}

// GetCommand 处理 GET /api/runtime/commands/{requestId}，返回命令当前状态
// （acceptedStatus 受理结论 + finalStatus 终态，未终态时为空）。
func (s *Server) GetCommand(w http.ResponseWriter, r *http.Request) {
	invoker, supported := s.Runtime.(CommandInvoker)
	if !supported {
		fail(w, http.StatusServiceUnavailable, "命令查询不可用")
		return
	}
	record, found := invoker.Command(chi.URLParam(r, "requestId"))
	if !found {
		fail(w, http.StatusNotFound, "命令记录不存在（可能已被截断）")
		return
	}
	ok(w, record)
}
