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
function runtimeMethods(rows) { return `<div class="table-responsive"><table class="table runtime-table"><thead><tr><th>服务</th><th>状态</th></tr></thead><tbody>${(rows || []).map(row => `<tr><td><strong>${escapeHtml(row.name)}</strong><div><code>${escapeHtml(row.key)}</code></div></td><td>${runtimeStatusBadge(row.status)}</td></tr>`).join('') || '<tr><td colspan="2" class="text-center text-muted py-4">暂无服务</td></tr>'}</tbody></table></div>`; }
function runtimeEvents(rows) { return `<div class="table-responsive"><table class="table runtime-table"><thead><tr><th>告警</th><th>级别</th><th>状态</th></tr></thead><tbody>${(rows || []).map(row => `<tr><td><strong>${escapeHtml(row.name)}</strong><div><code>${escapeHtml(row.key)}</code></div></td><td><span class="badge-pill ${(EVENT_LEVELS[row.level] || EVENT_LEVELS[0]).cls}">${(EVENT_LEVELS[row.level] || EVENT_LEVELS[0]).name}</span></td><td>${runtimeStatusBadge(row.status)}</td></tr>`).join('') || '<tr><td colspan="3" class="text-center text-muted py-4">暂无告警</td></tr>'}</tbody></table></div>`; }

function runtimeStatusBadge(status) {
  const labels = { available: '可用', degraded: '降级', unavailable: '未接入', unbound: '未绑定', invalid: '无效', stale: '过期', unmapped: '未映射', configured: '已配置', inactive: '未触发', pending: '等待触发', active: '已触发', good: '正常' };
  const value = status || 'unavailable';
  return `<span class="status-badge ${escapeHtml(value)}">${escapeHtml(labels[value] || value)}</span>`;
}

function runtimeValue(value) {
  if (value == null) return '<span class="text-muted">暂无数据</span>';
  return `<code>${escapeHtml(typeof value === 'object' ? JSON.stringify(value) : value)}</code>`;
}