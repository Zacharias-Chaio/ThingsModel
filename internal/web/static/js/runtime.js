// runtime.js - Normalized runtime data dashboard.

async function loadRuntimeDevices() {
  try {
    state.runtimeDevices = await RuntimeAPI.list() || [];
    if (!state.runtimeSelectedId && state.runtimeDevices.length) state.runtimeSelectedId = state.runtimeDevices[0].id;
    if (state.runtimeSelectedId && !state.runtimeDevices.some(device => device.id === state.runtimeSelectedId)) state.runtimeSelectedId = state.runtimeDevices[0] ? state.runtimeDevices[0].id : '';
  } catch (e) {
    state.runtimeDevices = [];
    toast(e.message, 'error');
  }
}

function renderRuntimeDevices() {
  const list = document.getElementById('runtime-device-list');
  const detail = document.getElementById('runtime-detail');
  if (!list || !detail) return;
  if (!state.runtimeDevices.length) {
    list.innerHTML = '<div class="empty-state"><i class="bi bi-activity"></i><div>暂无已加载设备，请先完成设备实例配置。</div></div>';
    detail.innerHTML = '';
    return;
  }
  list.innerHTML = state.runtimeDevices.map(device => `<button class="runtime-device-row ${device.id === state.runtimeSelectedId ? 'selected' : ''}" onclick="selectRuntimeDevice(decodeURIComponent('${encodeURIComponent(device.id)}'))"><span class="runtime-device-dot ${device.dataStatus === 'available' ? 'enabled' : ''}"></span><span class="min-w-0"><strong>${escapeHtml(device.name)}</strong><small>${escapeHtml(device.id)}</small></span>${runtimeStatusBadge(device.dataStatus)}</button>`).join('');
  renderRuntimeDetail();
}

function selectRuntimeDevice(id) { state.runtimeSelectedId = id; renderRuntimeDevices(); }
function showRuntimeTab(tab) { state.runtimeTab = tab; renderRuntimeDetail(); }

function renderRuntimeDetail() {
  const detail = document.getElementById('runtime-detail');
  const device = state.runtimeDevices.find(item => item.id === state.runtimeSelectedId);
  if (!device) { detail.innerHTML = ''; return; }
  const content = state.runtimeTab === 'properties' ? runtimeProperties(device.properties) : state.runtimeTab === 'methods' ? runtimeMethods(device.methods) : runtimeEvents(device.events);
  detail.innerHTML = `<div class="runtime-detail-head"><div><h5>${escapeHtml(device.name)}</h5><div class="text-muted small">${escapeHtml(device.id)} · 配置版本 ${device.configRevision}</div></div><div>${runtimeStatusBadge(device.dataStatus)}</div></div><div class="runtime-tabs"><button class="${state.runtimeTab === 'properties' ? 'active' : ''}" onclick="showRuntimeTab('properties')">属性</button><button class="${state.runtimeTab === 'methods' ? 'active' : ''}" onclick="showRuntimeTab('methods')">服务</button><button class="${state.runtimeTab === 'events' ? 'active' : ''}" onclick="showRuntimeTab('events')">告警</button></div>${content}`;
}

function runtimeProperties(rows) { return `<div class="table-responsive"><table class="table runtime-table"><thead><tr><th>属性</th><th>当前值</th><th>单位</th><th>质量</th></tr></thead><tbody>${(rows || []).map(row => `<tr><td><strong>${escapeHtml(row.name)}</strong><div><code>${escapeHtml(row.key)}</code></div></td><td>${runtimeValue(row.value)}</td><td>${escapeHtml(row.unit || '-')}</td><td>${runtimeStatusBadge(row.quality)}</td></tr>`).join('') || '<tr><td colspan="4" class="text-center text-muted py-4">暂无属性</td></tr>'}</tbody></table></div>`; }
function runtimeMethods(rows) {
  return `<div class="table-responsive"><table class="table runtime-table"><thead><tr><th>服务</th><th>状态</th><th>工程值下发</th></tr></thead><tbody>${(rows || []).map(row => `<tr><td><strong>${escapeHtml(row.name)}</strong><div><code>${escapeHtml(row.key)}</code></div>${row.targetProperty ? `<div class="text-muted small">→ ${escapeHtml(row.targetProperty)} · ${row.targetMode === 'logical' ? '逻辑点位' : '物理点位'}</div>` : ''}</td><td>${runtimeStatusBadge(row.status)}</td><td>${methodInvokeControl(row)}</td></tr>`).join('') || '<tr><td colspan="3" class="text-center text-muted py-4">暂无服务</td></tr>'}</tbody></table></div>`;
}

// 服务下发控件：number 填写工程值（带范围提示），status 选择状态。
// 下发链路：POST /runtime/devices/{id}/methods/{key} → 网关 .cmd（REQ/REP）→ cmdAck 终态轮询。
function methodInvokeControl(row) {
  if (row.status !== 'configured') return '<span class="text-muted">—</span>';
  const key = escapeHtml(row.key);
  const btn = `<button class="btn btn-primary btn-sm" onclick="invokeMethod('${key}')"><i class="bi bi-send"></i>下发</button>`;
  if (row.type === 'number') {
    const v = row.validation || {};
    const hint = v.min < v.max ? ` placeholder="${v.min} ~ ${v.max}"` : '';
    return `<div class="invoke-control"><input type="number" step="any" class="form-control form-control-sm" id="invoke-${key}"${hint}>${btn}</div>`;
  }
  if (row.type === 'status' && (row.descriptions || []).length) {
    const options = row.descriptions.map((d, i) => `<option value="${i}">${escapeHtml(d.name)}</option>`).join('');
    return `<div class="invoke-control"><select class="form-select form-select-sm" id="invoke-${key}">${options}</select>${btn}</div>`;
  }
  return '<span class="text-muted">—</span>';
}

// 读取、校验下发输入并发起真实下发：
// POST /runtime/devices/{id}/methods/{key} → 南向 .cmd REQ/REP（受理）→ cmdAck 异步终态轮询。
async function invokeMethod(key) {
  const device = state.runtimeDevices.find(item => item.id === state.runtimeSelectedId);
  const method = device && (device.methods || []).find(m => m.key === key);
  if (!device || !method) return;
  const el = document.getElementById('invoke-' + key);
  if (!el) return;
  let value = null;
  if (method.type === 'number') {
    value = parseFloat(el.value);
    if (el.value === '' || isNaN(value)) { toast('请输入数字工程值', 'error'); return; }
    const v = method.validation;
    if (v && v.min < v.max && (value < v.min || value > v.max)) { toast(`工程值需在 ${v.min} ~ ${v.max} 范围内`, 'error'); return; }
  } else {
    const desc = (method.descriptions || [])[parseInt(el.value, 10)];
    if (!desc) { toast('请选择状态值', 'error'); return; }
    value = parseFloat(desc.value);
    if (isNaN(value)) { toast(`状态「${desc.name}」未配置工程值，无法下发`, 'error'); return; }
  }
  let record;
  try {
    record = await RuntimeAPI.invoke(device.id, key, value);
  } catch (e) {
    toast(e.message, 'error');
    return;
  }
  // 逻辑点位：写入缓存即时终态，无需轮询网关
  if (record.targetKind === 'logical') {
    if (record.finalStatus === 'success') {
      toast(`${method.name} 已写入逻辑点位（${record.targetKey || ''} = ${value}）`);
    } else {
      toast(`写入逻辑点位失败：${record.finalMessage || '未知原因'}`, 'error');
    }
    return;
  }
  if (record.acceptedStatus !== 'accepted') {
    toast(`网关拒绝指令：${record.acceptedMessage || record.acceptedStatus || '未知原因'}`, 'error');
    return;
  }
  toast(`指令已受理（${method.name} = ${value}），等待网关执行回报…`);
  pollCommandResult(record.requestId, method.name);
}

// 轮询命令终态：cmdAck 由网关复用 .data 主题异步回报，最长等待 32s。
async function pollCommandResult(requestId, methodName) {
  for (let i = 0; i < 16; i++) {
    await new Promise(resolve => setTimeout(resolve, 2000));
    let record;
    try {
      record = await RuntimeAPI.command(requestId);
    } catch (e) {
      continue; // 单次轮询失败（如命令记录截断）不中断等待
    }
    if (record.finalStatus === 'success') { toast(`${methodName} 指令执行成功`); return; }
    if (record.finalStatus === 'failure') { toast(`${methodName} 指令执行失败：${record.finalMessage || '网关回报失败'}`, 'error'); return; }
  }
  toast(`${methodName} 执行回报超时，请检查网关状态`, 'error');
}
function runtimeEvents(rows) {
  return `<div class="table-responsive"><table class="table runtime-table"><thead><tr><th>监测点位</th><th>点位名称</th><th>当前值</th><th>告警规则简要</th><th>级别</th><th>状态</th></tr></thead><tbody>${(rows || []).map(row => {
    const rulesHtml = (row.rules || []).map(rule => `<div class="alarm-rule-brief">${escapeHtml(rule)}</div>`).join('');
    const methodChip = row.method === 'and' ? '<span class="method-chip">全部成立</span>' : row.method === 'or' ? '<span class="method-chip">任一成立</span>' : '';
    const level = EVENT_LEVELS[row.level] || EVENT_LEVELS[0];
    return `<tr><td><strong>${escapeHtml(row.point || '-')}</strong><div><code>${escapeHtml(row.key)}</code></div></td><td>${row.pointName ? escapeHtml(row.pointName) : '-'}</td><td>${runtimeValue(row.value)}</td><td>${rulesHtml ? rulesHtml + methodChip : '<span class="text-muted">-</span>'}</td><td><span class="badge-pill ${level.cls}">${level.name}</span></td><td>${runtimeStatusBadge(row.status)}</td></tr>`;
  }).join('') || '<tr><td colspan="6" class="text-center text-muted py-4">暂无告警</td></tr>'}</tbody></table></div>`;
}

function runtimeStatusBadge(status) {
  const labels = { available: '可用', degraded: '降级', unavailable: '未接入', unbound: '未绑定', invalid: '无效', stale: '过期', unmapped: '未映射', configured: '已配置', inactive: '未触发', pending: '等待触发', active: '已触发', good: '正常', default: '默认值' };
  const value = status || 'unavailable';
  return `<span class="status-badge ${escapeHtml(value)}">${escapeHtml(labels[value] || value)}</span>`;
}

function runtimeValue(value) {
  if (value == null) return '<span class="text-muted">暂无数据</span>';
  return `<code>${escapeHtml(typeof value === 'object' ? JSON.stringify(value) : value)}</code>`;
}