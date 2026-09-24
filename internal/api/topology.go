// topology.go - 网关拓扑发现与绑定校验 API。
package api

import (
	"context"
	"net/http"
	"time"

	"thingsmodel/internal/natsclient"
	"thingsmodel/internal/runtime"
)

// TopologyFacade 暴露南向拓扑能力，保持 handler 与 Manager 实现解耦。
type TopologyFacade interface {
	QueryTopology(ctx context.Context, gatewayID string) (*natsclient.MessageQueryResp, error)
	RefreshTopology(ctx context.Context) []runtime.GatewayTopology
	CheckBindings(ctx context.Context) ([]runtime.BindingCheckItem, error)
}

// topologyTimeout 单次 API 触发的拓扑查询总预算（多网关串行）。
const topologyTimeout = 15 * time.Second

// GetTopology 发现网关拓扑：不带 gatewayId 时查询全部订阅网关。
// 前端"发现设备"按钮调用此接口，把拓扑合并进来源目录供绑定选择。
func (s *Server) GetTopology(w http.ResponseWriter, r *http.Request) {
	facade, supported := s.Runtime.(TopologyFacade)
	if !supported {
		fail(w, http.StatusServiceUnavailable, "拓扑查询不可用")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), topologyTimeout)
	defer cancel()
	gatewayID := r.URL.Query().Get("gatewayId")
	if gatewayID != "" {
		result, err := facade.QueryTopology(ctx, gatewayID)
		if err != nil {
			fail(w, http.StatusServiceUnavailable, "查询网关拓扑失败: "+err.Error())
			return
		}
		ok(w, result)
		return
	}
	results := facade.RefreshTopology(ctx)
	if len(results) == 0 {
		fail(w, http.StatusServiceUnavailable, "未配置任何网关订阅，请先在平台设置中添加")
		return
	}
	// 部分网关失败仍返回整体结果，逐网关带失败原因，由前端提示。
	ok(w, results)
}

// CheckBindings 把所有已配置绑定与网关最新拓扑比对，报告失效引用。
// 触发时机：用户在设备管理页点击"绑定校验"。
func (s *Server) CheckBindings(w http.ResponseWriter, r *http.Request) {
	facade, supported := s.Runtime.(TopologyFacade)
	if !supported {
		fail(w, http.StatusServiceUnavailable, "绑定校验不可用")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), topologyTimeout)
	defer cancel()
	items, err := facade.CheckBindings(ctx)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	summary := map[string]int{"total": len(items), "ok": 0, "invalid": 0, "unchecked": 0, "unreachable": 0}
	for _, item := range items {
		switch item.Status {
		case "ok":
			summary["ok"]++
		case "unchecked":
			summary["unchecked"]++
		case "gateway_unreachable":
			summary["unreachable"]++
		default:
			summary["invalid"]++
		}
	}
	ok(w, map[string]any{"summary": summary, "items": items})
}
