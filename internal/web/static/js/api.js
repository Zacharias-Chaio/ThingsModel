// api.js — API 客户端封装
// 遵循 frontend-backend-collaboration.md §4.4：所有请求走 apiReq，不直接 fetch

const API_BASE = '/api';

// 通用请求封装：成功返回 data，失败 throw Error(msg)
async function apiReq(method, path, body) {
  const opt = { method, headers: {} };
  if (body !== undefined) {
    opt.headers['Content-Type'] = 'application/json';
    opt.body = JSON.stringify(body);
  }
  const res = await fetch(API_BASE + path, opt);
  const payload = await res.json().catch(() => ({ code: res.status, msg: '响应解析失败' }));
  if (!res.ok || payload.code !== 0) {
    throw new Error(payload.msg || ('HTTP ' + res.status));
  }
  return payload.data;
}

const apiGet    = (path)           => apiReq('GET',    path);
const apiPost    = (path, body)     => apiReq('POST',   path, body);
const apiDelete  = (path)          => apiReq('DELETE', path);

// ===== 模板 API =====
const TemplatesAPI = {
  list:   ()             => apiGet('/templates'),
  get:    (code)         => apiGet('/templates/' + encodeURIComponent(code)),
  save:   (template)     => apiPost('/templates', template),
  remove: (code)         => apiDelete('/templates/' + encodeURIComponent(code)),
};

const DevicesAPI = {
  list:   ()         => apiGet('/devices'),
  get:    (id)       => apiGet('/devices/' + encodeURIComponent(id)),
  save:   (device)   => apiPost('/devices', device),
  remove: (id)       => apiDelete('/devices/' + encodeURIComponent(id)),
};

const RuntimeAPI = {
  list: ()    => apiGet('/runtime/devices'),
  sources: () => apiGet('/runtime/sources'),
  // 网关拓扑发现（不带 gatewayId 时查询全部订阅网关）
  topology: (gatewayId) => apiGet('/runtime/topology' + (gatewayId ? '?gatewayId=' + encodeURIComponent(gatewayId) : '')),
  // 绑定校验：把已配置绑定与网关最新拓扑比对
  bindingCheck: () => apiGet('/runtime/binding-check'),
  // 控制下发：服务点位写命令（REQ/REP 受理 + cmdAck 异步终态）
  invoke: (deviceId, methodKey, value) => apiPost(`/runtime/devices/${encodeURIComponent(deviceId)}/methods/${encodeURIComponent(methodKey)}`, { value }),
  // 命令状态查询（轮询终态）
  command: (requestId) => apiGet(`/runtime/commands/${encodeURIComponent(requestId)}`),
};

const SettingsAPI = {
  get:    ()           => apiGet('/settings'),
  save:   (settings)   => apiPost('/settings', settings),
  system: ()           => apiGet('/system-info'),
  restart: ()          => apiPost('/restart'),
};
