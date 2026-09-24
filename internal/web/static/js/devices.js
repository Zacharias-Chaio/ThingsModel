// devices.js - Device instance management.

async function loadDevices() {
  try {
    state.devices = await DevicesAPI.list() || [];
  } catch (e) {
    state.devices = [];
    toast(e.message, 'error');
  }
}

async function loadSources() {
  try {
    state.sources = await RuntimeAPI.sources() || [];
  } catch (e) {
    state.sources = [];
    toast(e.message, 'error');
  }
}

// ===== 网关拓扑发现：把 .query 拓扑合并进来源目录 =====
// 遥测驱动的来源目录在网关不发数据时为空；拓扑发现让绑定页立即可选，
// 也为已有设备补全缺失属性（网关调整挂载顺序后可重新发现并对照）。
async function discoverFromTopology() {
  let results;
  try {
    results = await RuntimeAPI.topology() || [];
  } catch (e) {
    toast(e.message, 'error');
    return;
  }
  const merged = mergeTopologyIntoSources(results);
  const failures = results.filter(r => !r.ok);
  if (merged.addedDevices || merged.addedProps) {
    toast(`拓扑发现完成：新增 ${merged.addedDevices} 台设备、${merged.addedProps} 个属性点${failures.length ? `（${failures.length} 个网关查询失败）` : ''}`);
  } else if (failures.length === results.length) {
    toast('所有网关拓扑查询失败：' + (failures[0].message || '未知错误'), 'error');
  } else {
    toast('拓扑与当前来源目录一致，无新增设备' + (failures.length ? `（${failures.length} 个网关查询失败）` : ''));
  }
  if (state.deviceStep >= 1 && state.deviceDraft) renderDeviceWizard();
}

function mergeTopologyIntoSources(results) {
  let addedDevices = 0, addedProps = 0;
  results.forEach(result => {
    if (!result.ok || !result.topology) return;
    const gatewayId = result.gatewayId;
    (result.topology.channels || []).forEach(channel => {
      (channel.devices || []).forEach(device => {
        let source = state.sources.find(s => s.gatewayId === gatewayId && s.channelIndex === channel.channelIndex && s.deviceIndex === device.index);
        if (!source) {
          source = { gatewayId, gatewaySn: '', channelIndex: channel.channelIndex, deviceIndex: device.index, deviceName: device.name, commNo: 0, modelId: '', modelName: channel.name || '拓扑发现', lastSeen: null, properties: [] };
          state.sources.push(source);
          addedDevices++;
        }
        (device.datasheet || []).forEach(entry => {
          if (!source.properties.some(p => p.id === entry.data_id)) {
            source.properties.push({ id: entry.data_id, name: entry.data_name, accessMode: entry.data_rw });
            addedProps++;
          }
        });
      });
    });
  });
  return { addedDevices, addedProps };
}

// ===== 绑定校验：已配置绑定 vs 网关最新拓扑 =====
// 校验结论以单行横向标签展示在设备列表上方，不用弹窗、不渲染列表。
async function checkDeviceBindings() {
  renderBindingBanner('loading');
  let data;
  try {
    data = await RuntimeAPI.bindingCheck();
  } catch (e) {
    renderBindingBanner('error', '绑定校验失败：' + e.message);
    return;
  }
  const summary = data.summary || { total: 0, ok: 0, invalid: 0, unchecked: 0, unreachable: 0 };
  if (!summary.total) {
    renderBindingBanner('ok', '尚无已配置的绑定，无需校验');
    return;
  }
  if (summary.invalid === 0 && summary.unreachable === 0) {
    renderBindingBanner('ok', `绑定校验通过：${summary.ok}/${summary.total} 条引用有效${summary.unchecked ? `，${summary.unchecked} 条无法校验属性` : ''}`);
  } else {
    renderBindingBanner('warn', `绑定校验发现异常：有效 ${summary.ok} · 失效 ${summary.invalid} · 网关不可达 ${summary.unreachable}（共 ${summary.total} 条）。请检查后重新绑定失效项`);
  }
}

// 渲染单行结论横幅：loading=校验中，ok=通过，warn=有异常，error=请求失败。
function renderBindingBanner(kind, text) {
  const banner = document.getElementById('binding-check-banner');
  if (!banner) return;
  if (kind === 'loading') {
    banner.innerHTML = '<div class="binding-banner loading"><i class="bi bi-arrow-repeat me-2"></i>正在查询各网关拓扑并校验绑定…</div>';
    return;
  }
  const icons = { ok: 'bi-check-circle-fill', warn: 'bi-exclamation-triangle-fill', error: 'bi-x-circle-fill' };
  banner.innerHTML = `<div class="binding-banner ${kind}"><i class="bi ${icons[kind] || 'bi-info-circle'} me-2"></i>${escapeHtml(text || '')}</div>`;
}

function renderDeviceList() {
  const grid = document.getElementById('device-grid');
  if (!grid) return;
  document.getElementById('device-count').textContent = state.devices.length;
  if (state.devices.length === 0) {
    grid.innerHTML = '<div class="empty-state"><i class="bi bi-hdd-stack"></i><div>暂无设备实例，请从物模型模板创建设备。</div></div>';
    return;
  }
  grid.innerHTML = state.devices.map(device => {
    const progress = deviceBindingProgress(device);
    const encodedID = encodeURIComponent(device.id);
    return `
      <article class="device-card">
        <div class="device-card-top">
          <div class="device-card-icon"><i class="bi bi-hdd-network"></i></div>
          <div class="min-w-0 flex-grow-1">
            <div class="d-flex justify-content-between gap-2 align-items-start">
              <div class="model-card-title">${escapeHtml(device.name)}</div>
              <span class="status-badge ${device.enabled ? 'enabled' : 'disabled'}">${device.enabled ? '已启用' : '已停用'}</span>
            </div>
            <div class="model-card-sub">${escapeHtml(device.id)} · ${escapeHtml(device.templateCode)} · v${escapeHtml(device.templateVersion || '-')}</div>
          </div>
        </div>
        <div class="model-card-desc">${escapeHtml(device.description || '暂无描述')}</div>
        <div class="model-card-stats">
          <div class="mc-stat"><div class="num">${(device.properties || []).length}</div><div class="lbl">属性</div></div>
          <div class="mc-stat"><div class="num">${(device.methods || []).length}</div><div class="lbl">服务</div></div>
          <div class="mc-stat"><div class="num">${(device.events?.rule || []).length}</div><div class="lbl">告警</div></div>
        </div>
        <div class="binding-progress"><span>绑定完成度</span><strong>${progress.done}/${progress.total}</strong></div>
        <div class="progress" role="progressbar" aria-label="绑定完成度" aria-valuenow="${progress.percent}" aria-valuemin="0" aria-valuemax="100"><div class="progress-bar" style="width:${progress.percent}%"></div></div>
        <div class="model-card-actions mt-3">
          <button class="btn btn-outline-primary btn-sm" onclick="editDevice(decodeURIComponent('${encodedID}'))"><i class="bi bi-pencil me-1"></i>配置</button>
          <button class="btn btn-outline-secondary btn-sm" onclick="exportDevice(decodeURIComponent('${encodedID}'))"><i class="bi bi-download"></i></button>
          <button class="btn btn-outline-danger btn-sm" onclick="deleteDevice(decodeURIComponent('${encodedID}'))"><i class="bi bi-trash"></i></button>
        </div>
      </article>`;
  }).join('');
}

function deviceBindingProgress(device) {
  const properties = device.properties || [];
  const methods = device.methods || [];
  const alarmBindings = (device.events && device.events.binding) || [];
  const done = properties.filter(propertyConfigured).length + methods.filter(methodConfigured).length + alarmBindings.filter(eventConfigured).length;
  const total = properties.length + methods.length + alarmBindings.length;
  return { done, total, percent: total ? Math.round(done / total * 100) : 100 };
}

function sourceConfigured(source) {
  return !!(source && source.gatewayId && source.gatewayId.trim() && source.propertyId && source.propertyId.trim());
}

function propertyConfigured(property) {
  return !!(property.binding && property.binding.method && (property.binding.sources || []).length && property.binding.sources.every(sourceConfigured));
}

function methodConfigured(method) {
  return sourceConfigured(method.binding);
}

function eventConfigured(binding) {
  return !!(binding && sourceConfigured(binding.point) && (binding.rules || []).filter(Boolean).length);
}

async function newDevice() {
  if (!state.templates.length) await loadTemplates();
  await Promise.all([loadSources(), loadDevices()]);
  if (!state.templates.length) {
    toast('请先创建物模型模板', 'error');
    switchSection('templates');
    return;
  }
  state.deviceDraft = emptyDeviceDraft();
  // 设备实例 ID 默认自动生成：Device-001、Device-002 ...，不可修改
  state.deviceDraft.id = nextDeviceId(state.devices);
  state.deviceStep = 0;
  state.isEditingDevice = false;
  enterDeviceWizard();
}

// 生成下一个设备实例 ID：扫描已有 Device-XXX 编号取最大值 +1，格式 Device-001
function nextDeviceId(devices) {
  let max = 0;
  (devices || []).forEach(device => {
    const match = /^Device-(\d+)$/i.exec(String(device.id || '').trim());
    if (match) max = Math.max(max, parseInt(match[1], 10));
  });
  return 'Device-' + String(max + 1).padStart(3, '0');
}

async function editDevice(id) {
  try {
    const result = await Promise.all([DevicesAPI.get(id), loadSources()]);
    state.deviceDraft = normalizeDeviceDraft(result[0]);
    state.deviceStep = 0;
    state.isEditingDevice = true;
    enterDeviceWizard();
  } catch (e) {
    toast(e.message, 'error');
  }
}

// 进入设备向导：切换区块并刷新标题栏
function enterDeviceWizard() {
  switchSection('device-wizard');
  document.getElementById('device-wizard-title').textContent = state.deviceDraft.name || '新建设备实例';
  renderDeviceWizard();
}

// ===== 返回设备列表 =====
function backToDeviceList() {
  // 返回列表只放弃当前编辑缓冲，不持久化；只有保存按钮才会写入数据库
  state.deviceDraft = emptyDeviceDraft();
  state.deviceStep = 0;
  state.isEditingDevice = false;
  switchSection('devices');
}

function normalizeDeviceDraft(device) {
  const draft = JSON.parse(JSON.stringify(device));
  draft.properties = draft.properties || [];
  draft.methods = draft.methods || [];
  draft.events = normalizeDeviceEvents(draft.events);
  draft.properties.forEach(property => {
    property.binding = normalizePropertyBinding(property.binding);
  });
  draft.methods.forEach(method => method.binding = method.binding || { gatewayId: '', channelId: -1, deviceId: -1, propertyId: '' });
  return draft;
}

// 告警配置归一化：rule 为模板规则快照；binding 为检测点位关联（点位 + 判断方法 + 规则 key 列表）
function normalizeDeviceEvents(events) {
  events = events || {};
  events.rule = events.rule || [];
  events.binding = (events.binding || []).map(binding => ({
    point: {
      gatewayId: (binding.point && binding.point.gatewayId) || '',
      channelId: binding.point && typeof binding.point.channelId === 'number' ? binding.point.channelId : -1,
      deviceId: binding.point && typeof binding.point.deviceId === 'number' ? binding.point.deviceId : -1,
      propertyId: (binding.point && binding.point.propertyId) || ''
    },
    method: binding.method || 'or',
    rules: binding.rules || []
  }));
  return events;
}

// 属性绑定归一化：方法默认 ept；来源为网关/通道/设备/属性 四级定位，允许留空（未绑定）
function normalizePropertyBinding(binding) {
  binding = binding || {};
  binding.method = binding.method || 'ept';
  binding.sources = (binding.sources || []).map(source => ({
    gatewayId: source.gatewayId || '',
    channelId: typeof source.channelId === 'number' ? source.channelId : -1,
    deviceId: typeof source.deviceId === 'number' ? source.deviceId : -1,
    propertyId: source.propertyId || ''
  }));
  if (!binding.sources.length) binding.sources.push({ gatewayId: '', channelId: -1, deviceId: -1, propertyId: '' });
  return binding;
}

function applyDeviceTemplate(code) {
  const template = state.templates.find(item => item.code === code);
  if (!template) return;
  const draft = JSON.parse(JSON.stringify(template));
  // 仅覆盖模板相关字段，保留用户已输入的档案信息（id/name/description/enabled）
  state.deviceDraft.templateCode = template.code;
  state.deviceDraft.templateVersion = template.version || '';
  state.deviceDraft.properties = (draft.properties || []).map(property => ({ ...property, binding: normalizePropertyBinding(null) }));
  state.deviceDraft.methods = (draft.methods || []).map(method => ({ ...method, binding: { gatewayId: '', channelId: -1, deviceId: -1, propertyId: '' } }));
  state.deviceDraft.events = { rule: draft.events || [], binding: [] };
  renderDeviceWizard();
}

function renderDeviceWizard() {
  const step = DEVICE_WIZARD_STEPS[state.deviceStep];
  const stepper = document.getElementById('device-stepper');
  stepper.innerHTML = DEVICE_WIZARD_STEPS.map((item, index) => `
    <div class="step-item ${index === state.deviceStep ? 'active' : index < state.deviceStep ? 'done' : ''}"><div class="step-circle">${index + 1}</div><div class="step-label">${item.title}</div></div>${index < DEVICE_WIZARD_STEPS.length - 1 ? '<div class="step-connector ' + (index < state.deviceStep ? 'done' : '') + '"></div>' : ''}`).join('');
  document.getElementById('device-wizard-card').innerHTML = `
    <div class="step-heading"><div class="step-icon"><i class="bi ${step.icon}"></i></div><div><h5>${step.title}</h5><div class="text-muted small">${step.sub}</div></div></div>
    <div class="step-body">${deviceStepBody(state.deviceStep)}</div>`;
  document.getElementById('device-step-counter').textContent = `第 ${state.deviceStep + 1} 步 / 共 ${DEVICE_WIZARD_STEPS.length} 步`;
  document.getElementById('device-prev').disabled = state.deviceStep === 0;
  document.getElementById('device-next').disabled = state.deviceStep === DEVICE_WIZARD_STEPS.length - 1;
  if (state.deviceStep === 0) bindDeviceProfile();
}

function deviceStepBody(step) {
  if (step === 0) return deviceProfileBody();
  if (step === 1) return devicePropertiesBody();
  if (step === 2) return deviceMethodsBody();
  if (step === 3) return deviceEventsBody();
  return devicePreviewBody();
}

function deviceProfileBody() {
  const draft = state.deviceDraft;
  const options = state.templates.map(template => `<option value="${escapeHtml(template.code)}" ${template.code === draft.templateCode ? 'selected' : ''}>${escapeHtml(template.name)} (${escapeHtml(template.code)})</option>`).join('');
  return `<div class="info-banner"><i class="bi bi-info-circle-fill me-2" style="color:var(--primary)"></i><span class="text-muted">设备实例会保存模板快照与实际点位映射；后续模板修改不会自动覆盖已配置设备。</span></div>
    <div class="row g-3">
      <div class="col-md-6"><label class="form-label fw-semibold">设备实例 ID <span class="text-danger">*</span></label><input class="form-control" id="device-id" readonly value="${escapeHtml(draft.id)}" placeholder="如：Device-001"></div>
      <div class="col-md-6"><label class="form-label fw-semibold">设备名称 <span class="text-danger">*</span></label><input class="form-control" id="device-name" value="${escapeHtml(draft.name)}" placeholder="如：A区1号储能柜 PCS"></div>
      <div class="col-md-6"><label class="form-label fw-semibold">物模型模板 <span class="text-danger">*</span></label><select class="form-select" id="device-template" ${state.isEditingDevice ? 'disabled' : ''} onchange="applyDeviceTemplate(this.value)"><option value="">请选择模板</option>${options}</select></div>
      <div class="col-md-6"><label class="form-label fw-semibold">状态</label><select class="form-select" id="device-enabled"><option value="true" ${draft.enabled ? 'selected' : ''}>启用</option><option value="false" ${!draft.enabled ? 'selected' : ''}>停用</option></select></div>
      <div class="col-12"><label class="form-label fw-semibold">描述</label><textarea class="form-control" id="device-description" rows="2" placeholder="设备部署位置、用途等">${escapeHtml(draft.description)}</textarea></div>
    </div>`;
}

function bindDeviceProfile() {
  // 设备实例 ID 由系统自动生成（device-id 只读），不再绑定输入事件
  [['device-name', 'name'], ['device-description', 'description']].forEach(([id, field]) => {
    const element = document.getElementById(id);
    if (element) element.addEventListener('input', () => state.deviceDraft[field] = element.value);
  });
  document.getElementById('device-enabled').addEventListener('change', event => state.deviceDraft.enabled = event.target.value === 'true');
}

function devicePropertiesBody() {
  const rows = state.deviceDraft.properties.map((property, index) => {
    const binding = property.binding;
    const sources = binding.sources || [];
    const methods = property.type === 'status' ? ['ept'] : BINDING_METHODS;
    // 来源行（四级下拉）；删除列随来源数增减；添加列每个模板属性一个 + 号
    const sourceRows = sources.map((source, sourceIndex) => `
      <div class="binding-source-row">${sourceSelects(source, `updatePropertySource(${index},${sourceIndex}`)}</div>`).join('');
    const dels = sources.map((source, sourceIndex) => `
      <button class="btn btn-sm btn-outline-danger binding-del-btn" onclick="removePropertySource(${index},${sourceIndex})" title="删除来源"><i class="bi bi-trash"></i></button>`).join('');
    return `<tr><td><strong>${escapeHtml(property.name)}</strong><div><code>${escapeHtml(property.key)}</code></div></td><td>${typeBadge(property.type)}</td><td><select class="form-select form-select-sm" onchange="setPropertyBindingMethod(${index}, this.value)">${methods.map(method => `<option value="${method}" ${binding.method === method ? 'selected' : ''}>${method}</option>`).join('')}</select></td><td><div class="binding-source-list">${sourceRows}</div></td><td class="binding-del">${dels}</td><td class="binding-add"><button class="btn btn-sm btn-outline-secondary binding-add-btn" onclick="addPropertySource(${index})" title="添加来源"><i class="bi bi-plus-lg"></i></button></td></tr>`;
  }).join('');
  return `<div class="info-banner"><i class="bi bi-sliders me-2" style="color:var(--primary)"></i><span class="text-muted">状态属性仅支持 ept；数值属性可选择聚合方法并配置多个来源。</span><button class="btn btn-outline-primary btn-sm ms-auto flex-shrink-0" onclick="discoverFromTopology()" title="通过网关查询接口获取通道/设备拓扑，合并进来源目录"><i class="bi bi-diagram-3 me-1"></i>发现设备</button></div><div class="table-responsive"><table class="table binding-table align-middle"><thead><tr><th>模板属性</th><th>类型</th><th>方法</th><th>实际来源（网关 / 通道 / 设备 / 属性）</th><th class="text-center">删除</th><th class="text-center">添加</th></tr></thead><tbody>${rows || '<tr><td colspan="6" class="text-center text-muted py-4">模板没有属性</td></tr>'}</tbody></table></div>`;
}

// ===== 上游点位级联选择：网关ID → 通道ID → 设备ID → 属性ID =====
function sourceGateways() {
  return [...new Set(state.sources.map(s => s.gatewayId))].sort();
}

function sourceChannels(gatewayId) {
  return [...new Set(state.sources.filter(s => s.gatewayId === gatewayId).map(s => s.channelIndex))].sort((a, b) => a - b);
}

function sourceDeviceEntries(gatewayId, channelIndex) {
  return state.sources
    .filter(s => s.gatewayId === gatewayId && s.channelIndex === channelIndex)
    .sort((a, b) => a.deviceIndex - b.deviceIndex);
}

function findSourceDevice(gatewayId, channelIndex, deviceIndex) {
  return state.sources.find(s => s.gatewayId === gatewayId && s.channelIndex === channelIndex && s.deviceIndex === deviceIndex);
}

// 四级级联下拉（网关 / 通道 / 设备 / 属性），updatePrefix 形如 `updatePropertySource(${index},${sourceIndex}`
function sourceSelects(source, updatePrefix) {
  const gateways = sourceGateways();
  const gatewayKnown = gateways.includes(source.gatewayId);
  const channels = gatewayKnown ? sourceChannels(source.gatewayId) : [];
  const channelKnown = channels.includes(source.channelId);
  const devices = channelKnown ? sourceDeviceEntries(source.gatewayId, source.channelId) : [];
  const deviceKnown = devices.some(d => d.deviceIndex === source.deviceId);
  const src = deviceKnown ? findSourceDevice(source.gatewayId, source.channelId, source.deviceId) : null;
  const properties = (src && src.properties) || [];
  const propertyKnown = properties.some(p => p.id === source.propertyId);
  return `
    <select class="form-select form-select-sm" onchange="${updatePrefix},'gatewayId',this.value)">
      <option value="">网关ID</option>
      ${source.gatewayId && !gatewayKnown ? `<option value="${escapeHtml(source.gatewayId)}" selected>${escapeHtml(source.gatewayId)}（暂未发现）</option>` : ''}
      ${gateways.map(g => `<option value="${escapeHtml(g)}" ${g === source.gatewayId ? 'selected' : ''}>${escapeHtml(g)}</option>`).join('')}
    </select>
    <select class="form-select form-select-sm" onchange="${updatePrefix},'channelId',this.value)" ${gatewayKnown ? '' : 'disabled'}>
      <option value="">通道ID</option>
      ${channels.map(c => `<option value="${c}" ${c === source.channelId ? 'selected' : ''}>${c}</option>`).join('')}
    </select>
    <select class="form-select form-select-sm" onchange="${updatePrefix},'deviceId',this.value)" ${channelKnown ? '' : 'disabled'}>
      <option value="">设备ID</option>
      ${devices.map(d => `<option value="${d.deviceIndex}" ${d.deviceIndex === source.deviceId ? 'selected' : ''}>${d.deviceIndex}${(d.deviceName || d.modelName) ? ' · ' + escapeHtml(d.deviceName || d.modelName) : ''}</option>`).join('')}
    </select>
    <select class="form-select form-select-sm" onchange="${updatePrefix},'propertyId',this.value)" ${deviceKnown ? '' : 'disabled'}>
      <option value="">属性ID</option>
      ${source.propertyId && deviceKnown && !propertyKnown ? `<option value="${escapeHtml(source.propertyId)}" selected>${escapeHtml(source.propertyId)}（暂未发现）</option>` : ''}
      ${properties.map(p => `<option value="${escapeHtml(p.id)}" ${p.id === source.propertyId ? 'selected' : ''}>${escapeHtml(p.name || p.id)} (${escapeHtml(p.id)})</option>`).join('')}
    </select>`;
}

// 级联更新来源字段：上游变更时重置下游选择
function applySourceField(source, field, value) {
  if (field === 'gatewayId') {
    source.gatewayId = value;
    source.channelId = -1;
    source.deviceId = -1;
    source.propertyId = '';
  } else if (field === 'channelId') {
    source.channelId = value === '' ? -1 : parseInt(value, 10);
    source.deviceId = -1;
    source.propertyId = '';
  } else if (field === 'deviceId') {
    source.deviceId = value === '' ? -1 : parseInt(value, 10);
    source.propertyId = '';
  } else {
    source.propertyId = value;
  }
}

function setPropertyBindingMethod(index, method) {
  state.deviceDraft.properties[index].binding.method = method;
  renderDeviceWizard();
}

function addPropertySource(index) {
  const binding = state.deviceDraft.properties[index].binding;
  binding.sources.push({ gatewayId: '', channelId: -1, deviceId: -1, propertyId: '' });
  renderDeviceWizard();
}

function removePropertySource(index, sourceIndex) {
  state.deviceDraft.properties[index].binding.sources.splice(sourceIndex, 1);
  renderDeviceWizard();
}

function updatePropertySource(index, sourceIndex, field, value) {
  applySourceField(state.deviceDraft.properties[index].binding.sources[sourceIndex], field, value);
  renderDeviceWizard();
}

function deviceMethodsBody() {
  const rows = state.deviceDraft.methods.map((method, index) => {
    const binding = method.binding;
    return `<tr><td><strong>${escapeHtml(method.name)}</strong><div><code>${escapeHtml(method.key)}</code></div></td><td>${typeBadge(method.type)}</td><td colspan="2"><div class="binding-source-row">${sourceSelects(binding, `updateMethodBinding(${index}`)}</div></td></tr>`;
  }).join('');
  return `<div class="info-banner"><i class="bi bi-gear me-2" style="color:var(--primary)"></i><span class="text-muted">每个服务绑定一个实际下发点位。留空表示暂不启用该服务。</span></div><div class="table-responsive"><table class="table binding-table align-middle"><thead><tr><th>模板服务</th><th>类型</th><th colspan="2">实际下发点位（网关 / 通道 / 设备 / 属性）</th></tr></thead><tbody>${rows || '<tr><td colspan="4" class="text-center text-muted py-4">模板没有服务</td></tr>'}</tbody></table></div>`;
}

function updateMethodBinding(index, field, value) {
  applySourceField(state.deviceDraft.methods[index].binding, field, value);
  renderDeviceWizard();
}

// ===== 告警映射：检测点位 → 规则关联 =====
function alarmConditionText(rule) {
  const op = rule.type === 'equal' ? '=' : rule.type === 'upper' ? '>' : rule.type === 'lower' ? '<' : '?';
  return `${op} ${rule.threshold}`;
}

function alarmRuleLabel(rule) {
  const level = EVENT_LEVELS[rule.level] || EVENT_LEVELS[0];
  return `${rule.name} · ${level.name} · ${alarmConditionText(rule)}`;
}

function deviceEventsBody() {
  const events = state.deviceDraft.events;
  const rules = events.rule;
  const bindings = events.binding;
  if (!rules.length) {
    return `<div class="info-banner"><i class="bi bi-bell me-2" style="color:var(--primary)"></i><span class="text-muted">当前模板没有定义告警规则，请先在模板的告警配置中添加。</span></div>`;
  }
  const rows = bindings.map((binding, index) => {
    const ruleCount = binding.rules.filter(Boolean).length;
    const method = ruleCount >= 2 ? (['and', 'or'].includes(binding.method) ? binding.method : 'or') : 'ept';
    // 检测点位：四级级联 + 行内删除点位
    const pointRow = `<div class="binding-source-row has-remove">${sourceSelects(binding.point, `updateAlarmPoint(${index}`)}<button class="btn btn-sm btn-outline-danger" onclick="removeAlarmPoint(${index})" title="删除检测点位"><i class="bi bi-trash"></i></button></div>`;
    // 规则行：下拉选项过滤本关联中已选规则，避免重复
    const ruleRows = binding.rules.map((ruleKey, ruleIndex) => {
      const others = binding.rules.filter((_, i) => i !== ruleIndex);
      const options = rules.filter(rule => rule.key === ruleKey || !others.includes(rule.key)).map(rule => `<option value="${escapeHtml(rule.key)}" ${rule.key === ruleKey ? 'selected' : ''}>${escapeHtml(alarmRuleLabel(rule))}</option>`).join('');
      return `<select class="form-select form-select-sm" onchange="updateAlarmRule(${index},${ruleIndex},this.value)"><option value="">请选择告警规则</option>${options}</select>`;
    }).join('');
    // 删除列：与规则行一一对应；添加列：每个检测点位一个 + 号
    const dels = binding.rules.map((ruleKey, ruleIndex) => `<button class="btn btn-sm btn-outline-danger binding-del-btn" onclick="removeAlarmRule(${index},${ruleIndex})" title="删除规则关联"><i class="bi bi-trash"></i></button>`).join('');
    const methodSelect = ruleCount >= 2
      ? `<select class="form-select form-select-sm" onchange="updateAlarmMethod(${index},this.value)"><option value="and" ${method === 'and' ? 'selected' : ''}>and</option><option value="or" ${method === 'or' ? 'selected' : ''}>or</option></select>`
      : `<select class="form-select form-select-sm" disabled><option value="ept" selected>ept</option></select>`;
    return `<tr><td>${pointRow}</td><td>${methodSelect}</td><td><div class="binding-source-list">${ruleRows || '<div class="text-muted small">尚未关联规则</div>'}</div></td><td class="binding-del">${dels}</td><td class="binding-add"><button class="btn btn-sm btn-outline-secondary binding-add-btn" onclick="addAlarmRule(${index})" title="添加规则关联"><i class="bi bi-plus-lg"></i></button></td></tr>`;
  }).join('');
  return `<div class="info-banner"><i class="bi bi-bell me-2" style="color:var(--primary)"></i><span class="text-muted">一个检测点位可关联多条告警规则：单条规则 ept 直接判定；多条规则可选 and（全部成立）或 or（任一成立）。</span></div><div class="table-responsive"><table class="table binding-table align-middle"><thead><tr><th>检测点位（网关 / 通道 / 设备 / 属性）</th><th>判断方法</th><th>关联告警规则（模板）</th><th class="text-center">删除</th><th class="text-center">添加</th></tr></thead><tbody>${rows || '<tr><td colspan="5" class="text-center text-muted py-4">尚未配置检测点位</td></tr>'}</tbody></table></div><button class="btn btn-outline-secondary btn-sm mt-2" onclick="addAlarmPoint()"><i class="bi bi-plus-lg"></i> 添加检测点位</button>`;
}

function addAlarmPoint() {
  state.deviceDraft.events.binding.push({ point: { gatewayId: '', channelId: -1, deviceId: -1, propertyId: '' }, method: 'or', rules: [''] });
  renderDeviceWizard();
}
function removeAlarmPoint(index) { state.deviceDraft.events.binding.splice(index, 1); renderDeviceWizard(); }
function updateAlarmPoint(index, field, value) {
  applySourceField(state.deviceDraft.events.binding[index].point, field, value);
  renderDeviceWizard();
}
function addAlarmRule(index) { state.deviceDraft.events.binding[index].rules.push(''); renderDeviceWizard(); }
function removeAlarmRule(index, ruleIndex) { state.deviceDraft.events.binding[index].rules.splice(ruleIndex, 1); renderDeviceWizard(); }
function updateAlarmRule(index, ruleIndex, value) {
  state.deviceDraft.events.binding[index].rules[ruleIndex] = value;
  renderDeviceWizard();
}
function updateAlarmMethod(index, method) {
  state.deviceDraft.events.binding[index].method = method;
  renderDeviceWizard();
}

function devicePreviewBody() {
  const progress = deviceBindingProgress(state.deviceDraft);
  return `<div class="summary-grid"><div class="summary-card"><div class="val">${(state.deviceDraft.properties || []).length}</div><div class="lbl">属性数量</div></div><div class="summary-card"><div class="val">${(state.deviceDraft.methods || []).length}</div><div class="lbl">服务数量</div></div><div class="summary-card"><div class="val">${progress.done}/${progress.total}</div><div class="lbl">绑定完成度</div></div></div><div class="form-section-divider"><span><i class="bi bi-file-code me-1"></i>设备实例 JSON</span></div><pre class="code-preview">${escapeHtml(JSON.stringify(state.deviceDraft, null, 2))}</pre><div class="d-flex justify-content-end gap-2 mt-3"><button class="btn btn-outline-secondary" onclick="exportDeviceDraft()"><i class="bi bi-download me-1"></i>导出 JSON</button><button class="btn btn-success" onclick="saveDeviceDraft()"><i class="bi bi-database-check me-1"></i>保存设备</button></div>`;
}

function nextDeviceStep() {
  if (state.deviceStep === 0 && (!state.deviceDraft.id.trim() || !state.deviceDraft.name.trim() || !state.deviceDraft.templateCode)) {
    toast('请填写设备名称、设备实例 ID 并选择模板', 'error');
    return;
  }
  if (state.deviceStep < DEVICE_WIZARD_STEPS.length - 1) { state.deviceStep++; renderDeviceWizard(); }
}

function prevDeviceStep() { if (state.deviceStep > 0) { state.deviceStep--; renderDeviceWizard(); } }

async function saveDeviceDraft() {
  if (!state.deviceDraft.id.trim() || !state.deviceDraft.name.trim() || !state.deviceDraft.templateCode) {
    toast('请完善设备档案信息', 'error');
    return;
  }
  // 保存前清洗告警关联：过滤空规则行、剔除完全未配置的关联
  const payload = JSON.parse(JSON.stringify(state.deviceDraft));
  payload.events.binding = payload.events.binding
    .map(binding => ({ ...binding, rules: binding.rules.filter(Boolean) }))
    .filter(binding => (binding.point && binding.point.gatewayId) || binding.rules.length);
  try {
    await DevicesAPI.save(payload);
    await loadDevices();
    toast(state.isEditingDevice ? '设备已更新并热重载' : '设备已保存并热重载');
    switchSection('devices');
  } catch (e) {
    toast(e.message, 'error');
  }
}

async function deleteDevice(id) {
  const device = state.devices.find(item => item.id === id);
  if (!confirm(`确认删除设备「${device ? device.name : id}」？此操作不可恢复。`)) return;
  try {
    await DevicesAPI.remove(id);
    await loadDevices();
    renderDeviceList();
    toast('设备已删除并从运行配置中移除');
  } catch (e) {
    toast(e.message, 'error');
  }
}

async function exportDevice(id) {
  try {
    const device = await DevicesAPI.get(id);
    downloadFile(`${device.id}.json`, JSON.stringify(device, null, 2), 'application/json');
  } catch (e) { toast(e.message, 'error'); }
}

function exportDeviceDraft() {
  downloadFile(`${state.deviceDraft.id || 'device'}.json`, JSON.stringify(state.deviceDraft, null, 2), 'application/json');
}