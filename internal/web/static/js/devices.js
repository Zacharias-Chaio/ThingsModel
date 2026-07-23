// devices.js - Device instance management.

async function loadDevices() {
  try {
    state.devices = await DevicesAPI.list() || [];
  } catch (e) {
    state.devices = [];
    toast(e.message, 'error');
  }
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
          <div class="mc-stat"><div class="num">${(device.events || []).length}</div><div class="lbl">告警</div></div>
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
  const events = device.events || [];
  const done = properties.filter(propertyConfigured).length + methods.filter(methodConfigured).length + events.filter(eventConfigured).length;
  const total = properties.length + methods.length + events.length;
  return { done, total, percent: total ? Math.round(done / total * 100) : 100 };
}

function sourceConfigured(source) {
  return !!(source && source.deviceId && source.deviceId.trim() && source.propertyId && source.propertyId.trim());
}

function propertyConfigured(property) {
  return !!(property.binding && property.binding.method && (property.binding.sources || []).length && property.binding.sources.every(sourceConfigured));
}

function methodConfigured(method) {
  return sourceConfigured(method.binding);
}

function eventConfigured(event) {
  return !!((event.binding || []).length && event.binding.every(sourceConfigured));
}

async function newDevice() {
  if (!state.templates.length) await loadTemplates();
  if (!state.templates.length) {
    toast('请先创建物模型模板', 'error');
    switchSection('templates');
    return;
  }
  state.deviceDraft = emptyDeviceDraft();
  state.deviceStep = 0;
  state.isEditingDevice = false;
  switchSection('device-wizard');
  renderDeviceWizard();
}

async function editDevice(id) {
  try {
    state.deviceDraft = normalizeDeviceDraft(await DevicesAPI.get(id));
    state.deviceStep = 0;
    state.isEditingDevice = true;
    switchSection('device-wizard');
    renderDeviceWizard();
  } catch (e) {
    toast(e.message, 'error');
  }
}

function normalizeDeviceDraft(device) {
  const draft = JSON.parse(JSON.stringify(device));
  draft.properties = draft.properties || [];
  draft.methods = draft.methods || [];
  draft.events = draft.events || [];
  draft.properties.forEach(property => {
    property.binding = property.binding || { method: '', sources: [] };
    property.binding.sources = property.binding.sources || [];
  });
  draft.methods.forEach(method => method.binding = method.binding || { deviceId: '', propertyId: '' });
  draft.events.forEach(event => event.binding = event.binding || []);
  return draft;
}

function applyDeviceTemplate(code) {
  const template = state.templates.find(item => item.code === code);
  if (!template) return;
  const draft = JSON.parse(JSON.stringify(template));
  // 仅覆盖模板相关字段，保留用户已输入的档案信息（id/name/description/enabled）
  state.deviceDraft.templateCode = template.code;
  state.deviceDraft.templateVersion = template.version || '';
  state.deviceDraft.properties = (draft.properties || []).map(property => ({ ...property, binding: { method: '', sources: [] } }));
  state.deviceDraft.methods = (draft.methods || []).map(method => ({ ...method, binding: { deviceId: '', propertyId: '' } }));
  state.deviceDraft.events = (draft.events || []).map(event => ({ ...event, binding: [] }));
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
      <div class="col-md-6"><label class="form-label fw-semibold">设备名称 <span class="text-danger">*</span></label><input class="form-control" id="device-name" value="${escapeHtml(draft.name)}" placeholder="如：A区1号储能柜 PCS"></div>
      <div class="col-md-6"><label class="form-label fw-semibold">设备实例 ID <span class="text-danger">*</span></label><input class="form-control" id="device-id" ${state.isEditingDevice ? 'readonly' : ''} value="${escapeHtml(draft.id)}" placeholder="如：PCS-A01"></div>
      <div class="col-md-8"><label class="form-label fw-semibold">物模型模板 <span class="text-danger">*</span></label><select class="form-select" id="device-template" ${state.isEditingDevice ? 'disabled' : ''} onchange="applyDeviceTemplate(this.value)"><option value="">请选择模板</option>${options}</select></div>
      <div class="col-md-4"><label class="form-label fw-semibold">状态</label><select class="form-select" id="device-enabled"><option value="true" ${draft.enabled ? 'selected' : ''}>启用</option><option value="false" ${!draft.enabled ? 'selected' : ''}>停用</option></select></div>
      <div class="col-12"><label class="form-label fw-semibold">描述</label><textarea class="form-control" id="device-description" rows="2" placeholder="设备部署位置、用途等">${escapeHtml(draft.description)}</textarea></div>
    </div>`;
}

function bindDeviceProfile() {
  [['device-name', 'name'], ['device-id', 'id'], ['device-description', 'description']].forEach(([id, field]) => {
    const element = document.getElementById(id);
    if (element) element.addEventListener('input', () => state.deviceDraft[field] = element.value);
  });
  document.getElementById('device-enabled').addEventListener('change', event => state.deviceDraft.enabled = event.target.value === 'true');
}

function devicePropertiesBody() {
  const rows = state.deviceDraft.properties.map((property, index) => {
    const binding = property.binding || { method: '', sources: [] };
    const sources = binding.sources || [];
    const methods = property.type === 'enum' ? ['EPT'] : BINDING_METHODS;
    return `<tr><td><strong>${escapeHtml(property.name)}</strong><div><code>${escapeHtml(property.key)}</code></div></td><td>${typeBadge(property.type)}</td><td><select class="form-select form-select-sm" onchange="setPropertyBindingMethod(${index}, this.value)"><option value="">未绑定</option>${methods.map(method => `<option value="${method}" ${binding.method === method ? 'selected' : ''}>${method}</option>`).join('')}</select></td><td><div class="binding-source-list">${sources.map((source, sourceIndex) => bindingSourceRow(source, `updatePropertySource(${index},${sourceIndex}`, `removePropertySource(${index},${sourceIndex})`)).join('')}</div><button class="btn btn-outline-secondary btn-sm mt-1" onclick="addPropertySource(${index})"><i class="bi bi-plus-lg"></i> 来源</button></td></tr>`;
  }).join('');
  return `<div class="info-banner"><i class="bi bi-sliders me-2" style="color:var(--primary)"></i><span class="text-muted">枚举属性仅支持 EPT；数值属性可选择聚合方法并配置多个来源。</span></div><div class="table-responsive"><table class="table binding-table align-middle"><thead><tr><th>模板属性</th><th>类型</th><th>方法</th><th>实际来源（设备 ID / 属性 ID）</th></tr></thead><tbody>${rows || '<tr><td colspan="4" class="text-center text-muted py-4">模板没有属性</td></tr>'}</tbody></table></div>`;
}

function bindingSourceRow(source, updatePrefix, removeCall) {
  return `<div class="binding-source-row"><input class="form-control form-control-sm" value="${escapeHtml(source.deviceId || '')}" placeholder="实际设备 ID" oninput="${updatePrefix},'deviceId',this.value)"><input class="form-control form-control-sm" value="${escapeHtml(source.propertyId || '')}" placeholder="实际属性 ID" oninput="${updatePrefix},'propertyId',this.value)"><button class="btn btn-outline-danger btn-sm" onclick="${removeCall}"><i class="bi bi-x-lg"></i></button></div>`;
}

function setPropertyBindingMethod(index, method) {
  const binding = state.deviceDraft.properties[index].binding = state.deviceDraft.properties[index].binding || { method: '', sources: [] };
  binding.method = method;
  if (method && !binding.sources.length) binding.sources.push({ deviceId: '', propertyId: '' });
  if (!method) binding.sources = [];
  renderDeviceWizard();
}

function addPropertySource(index) {
  const binding = state.deviceDraft.properties[index].binding;
  if (!binding.method) { toast('请先选择聚合方法', 'error'); return; }
  binding.sources.push({ deviceId: '', propertyId: '' });
  renderDeviceWizard();
}

function removePropertySource(index, sourceIndex) {
  state.deviceDraft.properties[index].binding.sources.splice(sourceIndex, 1);
  renderDeviceWizard();
}

function updatePropertySource(index, sourceIndex, field, value) {
  state.deviceDraft.properties[index].binding.sources[sourceIndex][field] = value;
}

function deviceMethodsBody() {
  const rows = state.deviceDraft.methods.map((method, index) => {
    const binding = method.binding || {};
    return `<tr><td><strong>${escapeHtml(method.name)}</strong><div><code>${escapeHtml(method.key)}</code></div></td><td>${typeBadge(method.type)}</td><td><input class="form-control form-control-sm" value="${escapeHtml(binding.deviceId || '')}" placeholder="实际设备 ID" oninput="updateMethodBinding(${index},'deviceId',this.value)"></td><td><input class="form-control form-control-sm" value="${escapeHtml(binding.propertyId || '')}" placeholder="实际属性 ID" oninput="updateMethodBinding(${index},'propertyId',this.value)"></td></tr>`;
  }).join('');
  return `<div class="info-banner"><i class="bi bi-gear me-2" style="color:var(--primary)"></i><span class="text-muted">每个服务最多绑定一个实际下发点位。留空表示暂不启用该服务。</span></div><div class="table-responsive"><table class="table binding-table align-middle"><thead><tr><th>模板服务</th><th>类型</th><th>实际设备 ID</th><th>实际属性 ID</th></tr></thead><tbody>${rows || '<tr><td colspan="4" class="text-center text-muted py-4">模板没有服务</td></tr>'}</tbody></table></div>`;
}

function updateMethodBinding(index, field, value) {
  state.deviceDraft.methods[index].binding[field] = value;
}

function deviceEventsBody() {
  const rows = state.deviceDraft.events.map((event, index) => `<tr><td><strong>${escapeHtml(event.name)}</strong><div><code>${escapeHtml(event.key)}</code></div></td><td><span class="badge-pill ${(EVENT_LEVELS[event.level] || EVENT_LEVELS[0]).cls}">${(EVENT_LEVELS[event.level] || EVENT_LEVELS[0]).name}</span></td><td><div class="binding-source-list">${(event.binding || []).map((source, sourceIndex) => bindingSourceRow(source, `updateEventSource(${index},${sourceIndex}`, `removeEventSource(${index},${sourceIndex})`)).join('')}</div><button class="btn btn-outline-secondary btn-sm mt-1" onclick="addEventSource(${index})"><i class="bi bi-plus-lg"></i> 监测点</button></td></tr>`).join('');
  return `<div class="info-banner"><i class="bi bi-bell me-2" style="color:var(--primary)"></i><span class="text-muted">一个告警可关联多个监测点；留空表示暂不启用该告警。</span></div><div class="table-responsive"><table class="table binding-table align-middle"><thead><tr><th>模板告警</th><th>级别</th><th>实际监测点（设备 ID / 属性 ID）</th></tr></thead><tbody>${rows || '<tr><td colspan="3" class="text-center text-muted py-4">模板没有告警</td></tr>'}</tbody></table></div>`;
}

function addEventSource(index) { state.deviceDraft.events[index].binding.push({ deviceId: '', propertyId: '' }); renderDeviceWizard(); }
function removeEventSource(index, sourceIndex) { state.deviceDraft.events[index].binding.splice(sourceIndex, 1); renderDeviceWizard(); }
function updateEventSource(index, sourceIndex, field, value) { state.deviceDraft.events[index].binding[sourceIndex][field] = value; }

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
  try {
    await DevicesAPI.save(state.deviceDraft);
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