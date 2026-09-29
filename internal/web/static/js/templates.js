// templates.js — 模板管理模块
// 严格遵循 templats/模板说明.md 数据结构
// 遵循 web-design-guide.md 组件规范

// ===== 加载模板列表 =====
async function loadTemplates() {
  try {
    state.templates = await TemplatesAPI.list() || [];
  } catch (e) {
    state.templates = [];
    toast(e.message, 'error');
  }
}

// ===== 渲染模板卡片列表 =====
function renderTemplateList() {
  const grid = document.getElementById('template-grid');
  const list = state.templates;
  document.getElementById('tpl-count').textContent = list.length;

  if (list.length === 0) {
    grid.innerHTML = `
      <div class="empty-state">
        <i class="bi bi-inboxes"></i>
        <div>暂无物模型模板，点击右上角「新增物模型模板」开始配置。</div>
      </div>`;
    return;
  }

  grid.innerHTML = list.map(t => {
    const propCount = (t.properties || []).length;
    const methodCount = (t.methods || []).length;
    const eventCount = (t.events || []).length;
    return `
      <div class="model-card">
        <div class="model-card-top">
          <div class="model-card-icon"><i class="bi bi-box-seam"></i></div>
          <div class="min-w-0">
            <div class="model-card-title">${escapeHtml(t.name)}</div>
            <div class="model-card-sub">${escapeHtml(t.code)} · v${escapeHtml(t.version || '1.0.0')}</div>
          </div>
        </div>
        <div class="model-card-tags">
          ${t.category ? `<span class="mc-tag iface">${escapeHtml(t.category)}</span>` : ''}
        </div>
        <div class="model-card-desc">${escapeHtml(t.description || '暂无描述')}</div>
        <div class="model-card-stats">
          <div class="mc-stat"><div class="num">${propCount}</div><div class="lbl">属性</div></div>
          <div class="mc-stat"><div class="num">${methodCount}</div><div class="lbl">服务</div></div>
          <div class="mc-stat"><div class="num">${eventCount}</div><div class="lbl">告警</div></div>
        </div>
        <div class="model-card-actions">
          <button class="btn btn-outline-primary btn-sm" onclick='editTemplate(${JSON.stringify(t.code)})'>
            <i class="bi bi-pencil me-1"></i>编辑
          </button>
          <button class="btn btn-outline-secondary btn-sm" onclick='exportTemplate(${JSON.stringify(t.code)})'>
            <i class="bi bi-download me-1"></i>导出
          </button>
          <button class="btn btn-outline-danger btn-sm" onclick='deleteTemplate(${JSON.stringify(t.code)},${JSON.stringify(t.name)})'>
            <i class="bi bi-trash"></i>
          </button>
        </div>
      </div>`;
  }).join('');
}

// ===== 新增模板 =====
function newTemplate() {
  state.draft = emptyDraft();
  state.isEditing = false;
  state.currentStep = 0;
  enterTemplateWizard();
}

// ===== 编辑现有模板 =====
async function editTemplate(code) {
  try {
    const t = await TemplatesAPI.get(code);
    state.draft = JSON.parse(JSON.stringify(t)); // 深拷贝
    // 确保字段存在
    if (!state.draft.properties) state.draft.properties = [];
    if (!state.draft.methods) state.draft.methods = [];
    if (!state.draft.events) state.draft.events = [];
    state.isEditing = true;
    state.currentStep = 0;
    enterTemplateWizard();
  } catch (e) {
    toast(e.message, 'error');
  }
}

// 进入模板向导：切换区块并刷新标题栏
function enterTemplateWizard() {
  switchSection('wizard');
  document.getElementById('tpl-wizard-title').textContent = state.draft.name || '新增物模型模板';
  renderStepper();
  renderStep();
}

// ===== 返回模板列表 =====
function backToTemplateList() {
  // 返回列表只放弃当前编辑缓冲，不持久化；只有保存按钮才会写入 templats 目录
  state.draft = emptyDraft();
  state.isEditing = false;
  state.currentStep = 0;
  switchSection('templates');
}

// ===== 删除模板 =====
async function deleteTemplate(code, name) {
  if (!confirm(`确认删除模板「${name}」(${code})？此操作不可恢复。`)) return;
  try {
    await TemplatesAPI.remove(code);
    await loadTemplates();
    renderTemplateList();
    toast('模板已删除');
  } catch (e) {
    toast(e.message, 'error');
  }
}

// ===== 导出单个模板为 JSON 文件 =====
async function exportTemplate(code) {
  try {
    const t = await TemplatesAPI.get(code);
    const filename = (t.code || t.name || 'template') + '.json';
    downloadFile(filename, JSON.stringify(t, null, 4), 'application/json');
  } catch (e) {
    toast(e.message, 'error');
  }
}

// ===== Stepper 渲染 =====
function renderStepper() {
  const items = document.querySelectorAll('.step-item');
  const conns = document.querySelectorAll('.step-connector');
  items.forEach((it, idx) => {
    it.classList.remove('active', 'done');
    if (idx < state.currentStep) it.classList.add('done');
    else if (idx === state.currentStep) it.classList.add('active');
  });
  conns.forEach((c, idx) => c.classList.toggle('done', idx < state.currentStep));
  document.getElementById('step-counter').textContent = `第 ${state.currentStep + 1} 步 / 共 ${WIZARD_STEPS.length} 步`;
  document.getElementById('btn-prev').disabled = state.currentStep === 0;
  document.getElementById('btn-next').disabled = state.currentStep === WIZARD_STEPS.length - 1;
}

// ===== 渲染当前步骤内容 =====
function renderStep() {
  const m = WIZARD_STEPS[state.currentStep];
  const card = document.getElementById('wizard-card');
  card.innerHTML = `
    <div class="step-heading">
      <div class="step-icon"><i class="bi ${m.icon}"></i></div>
      <div class="flex-grow-1">
        <h5 class="fw-bold mb-0">${m.title}</h5>
        <div class="text-muted small">${m.sub}</div>
      </div>
    </div>
    <div class="step-body">${stepBody(state.currentStep)}</div>`;
  // 步骤特定的后渲染绑定
  if (state.currentStep === 0) bindProfileInputs();
  if (state.currentStep === 4) renderPreview();
}

function stepBody(step) {
  switch (step) {
    case 0: return profileBody();
    case 1: return propertiesBody();
    case 2: return methodsBody();
    case 3: return eventsBody();
    case 4: return previewBody();
  }
  return '';
}

// ===== 步骤 0: 档案信息 =====
function profileBody() {
  const p = state.draft;
  return `
    <div class="info-banner">
      <i class="bi bi-info-circle-fill me-2" style="color:var(--primary)"></i>
      <span class="text-muted">档案信息是物模型模板的基础元数据，<span class="fw-semibold">模板编码</span>与<span class="fw-semibold">模板名称</span>为必填项。支持导入已有 JSON 模板。</span>
    </div>
    <div class="d-flex justify-content-end mb-2">
      <label class="btn btn-outline-secondary btn-sm mb-0">
        <i class="bi bi-box-arrow-in-down me-1"></i>导入模板
        <input type="file" accept=".json" id="pf-import" hidden onchange="importTemplate(this)">
      </label>
    </div>
    <div class="form-section-divider"><span><i class="bi bi-folder2-open me-1"></i>档案信息</span></div>
    <div class="row g-3">
      <div class="col-md-6">
        <label class="form-label fw-semibold">模板编码 <span class="text-danger">*</span></label>
        <input type="text" class="form-control" id="pf-code" value="${escapeHtml(p.code)}" placeholder="如：PCS-DEVICE-001">
        <div class="invalid-feedback">请输入模板编码</div>
      </div>
      <div class="col-md-6">
        <label class="form-label fw-semibold">模板名称 <span class="text-danger">*</span></label>
        <input type="text" class="form-control" id="pf-name" value="${escapeHtml(p.name)}" placeholder="如：PCS 储能变流器">
        <div class="invalid-feedback">请输入模板名称</div>
      </div>
      <div class="col-md-6">
        <label class="form-label fw-semibold">模板分类</label>
        <input type="text" class="form-control" id="pf-category" value="${escapeHtml(p.category)}" placeholder="如：Inverter / BMS / 储能">
      </div>
      <div class="col-md-6">
        <label class="form-label fw-semibold">模板版本</label>
        <input type="text" class="form-control" id="pf-version" value="${escapeHtml(p.version)}" placeholder="如：1.0.0">
      </div>
      <div class="col-12">
        <label class="form-label fw-semibold">模板描述</label>
        <textarea class="form-control" id="pf-description" rows="2" placeholder="模板用途、适用设备等描述">${escapeHtml(p.description)}</textarea>
      </div>
    </div>`;
}

function bindProfileInputs() {
  const fields = { name: 'pf-name', code: 'pf-code', category: 'pf-category', version: 'pf-version', description: 'pf-description' };
  Object.keys(fields).forEach(k => {
    const el = document.getElementById(fields[k]);
    if (el) el.addEventListener('input', () => state.draft[k] = el.value);
  });
}

// 档案信息校验
function validateProfile() {
  let ok = true;
  [['pf-code', 'code'], ['pf-name', 'name']].forEach(([id, k]) => {
    const el = document.getElementById(id);
    if (!el || !state.draft[k] || !state.draft[k].trim()) {
      if (el) el.classList.add('is-invalid');
      ok = false;
    } else if (el) {
      el.classList.remove('is-invalid');
    }
  });
  return ok;
}

// 导入模板 JSON
async function importTemplate(input) {
  const file = input.files[0];
  if (!file) return;
  try {
    const text = await readFileText(file);
    const obj = JSON.parse(text);
    if (!obj.name || !obj.code) { toast('模板缺少 name 或 code 字段', 'error'); return; }
    state.draft = Object.assign(emptyDraft(), obj);
    if (!Array.isArray(state.draft.properties)) state.draft.properties = [];
    if (!Array.isArray(state.draft.methods)) state.draft.methods = [];
    if (!Array.isArray(state.draft.events)) state.draft.events = [];
    state.isEditing = true; // 导入视为编辑，保存时按 code 覆盖
    renderStep();
    toast('模板已导入，请检查后保存');
  } catch (e) {
    toast('导入失败：' + e.message, 'error');
  }
  input.value = '';
}

// ===== 步骤 1: 属性配置 =====
function propertiesBody() {
  const rows = state.draft.properties || [];
  let tbody = '';
  if (rows.length === 0) {
    tbody = `<tr><td colspan="7" class="text-center text-muted py-4">暂无属性，点击右上角「新增属性」添加</td></tr>`;
  } else {
    tbody = rows.map((r, i) => `
      <tr>
        <td>${r.number}</td>
        <td><code>${escapeHtml(r.key)}</code></td>
        <td>${escapeHtml(r.name)}</td>
        <td>${typeBadge(r.type)}</td>
        <td>${escapeHtml(r.unit || '-')}</td>
        <td>${escapeHtml(r.default == null ? '' : String(r.default))}</td>
        <td class="text-end">
          <button class="btn btn-sm btn-outline-primary" onclick="editProperty(${i})"><i class="bi bi-pencil"></i></button>
          <button class="btn btn-sm btn-outline-danger" onclick="removeProperty(${i})"><i class="bi bi-trash"></i></button>
        </td>
      </tr>`).join('');
  }
  return `
    <div class="info-banner">
      <i class="bi bi-info-circle-fill me-2" style="color:var(--primary)"></i>
      <span class="text-muted">属性定义设备<span class="fw-semibold">数据点</span>。<span class="fw-semibold">数据类型(number)</span>可聚合，<span class="fw-semibold">状态类型(status)</span>须配置状态值。</span>
    </div>
    <div class="d-flex justify-content-end mb-2">
      <button class="btn btn-primary btn-sm" data-bs-toggle="modal" data-bs-target="#propModal" onclick="resetPropertyModal()"><i class="bi bi-plus-lg me-1"></i>新增属性</button>
    </div>
    <div class="table-responsive">
      <table class="table table-hover align-middle">
        <thead><tr>
          <th>编号</th><th>标识符</th><th>属性名称</th><th>类型</th><th>单位</th><th>默认值</th><th class="text-end">操作</th>
        </tr></thead>
        <tbody>${tbody}</tbody>
      </table>
    </div>`;
}

function typeBadge(type) {
  if (type === 'status') return `<span class="badge-pill badge-r">status</span>`;
  if (type === 'number') return `<span class="badge-pill badge-rw">number</span>`;
  return `<span class="badge-pill badge-w">${escapeHtml(type || '-')}</span>`;
}

// 属性编辑：弹出 Modal，使用临时索引记录编辑对象
let _editingPropIndex = -1;

// 计算下一个编号：从 0 开始自动递增（0,1,2...），适用于属性/服务/告警
function nextRowNumber(rows) {
  if (!rows || rows.length === 0) return 0;
  return Math.max(...rows.map(r => (typeof r.number === 'number' ? r.number : -1))) + 1;
}

function resetPropertyModal() {
  _editingPropIndex = -1;
  document.getElementById('prop-number').value = nextRowNumber(state.draft.properties);
  document.getElementById('prop-key').value = '';
  document.getElementById('prop-name').value = '';
  document.getElementById('prop-default').value = '';
  document.getElementById('prop-unit').value = '';
  document.getElementById('prop-type').value = 'number';
  document.getElementById('prop-desc-list').innerHTML = '';
  onPropTypeChange();
}

function editProperty(index) {
  const r = state.draft.properties[index];
  _editingPropIndex = index;
  document.getElementById('prop-number').value = r.number;
  document.getElementById('prop-name').value = r.name || '';
  document.getElementById('prop-key').value = r.key || '';
  document.getElementById('prop-default').value = (r.default == null ? '' : String(r.default));
  document.getElementById('prop-unit').value = r.unit || '';
  document.getElementById('prop-type').value = r.type || 'number';
  // 状态值描述
  const dl = document.getElementById('prop-desc-list');
  dl.innerHTML = (r.description || []).map((d, i) => statusDescRow(d, i)).join('') || '';
  onPropTypeChange();
  new bootstrap.Modal(document.getElementById('propModal')).show();
}

function addProperty() {
  const prop = {
    number: parseInt(document.getElementById('prop-number').value) || 0,
    name: document.getElementById('prop-name').value.trim(),
    key: document.getElementById('prop-key').value.trim(),
    default: parseDefault(document.getElementById('prop-default').value, document.getElementById('prop-type').value),
    unit: document.getElementById('prop-unit').value.trim(),
    type: document.getElementById('prop-type').value,
    description: collectStatusDesc('prop-desc-list')
  };
  if (!prop.name || !prop.key) { toast('请填写属性名称和标识符', 'error'); return; }
  if (prop.type === 'status' && !prop.description.length) { toast('状态类型(status)至少配置一个状态值', 'error'); return; }
  if (_editingPropIndex >= 0) state.draft.properties[_editingPropIndex] = prop;
  else state.draft.properties.push(prop);
  bootstrap.Modal.getInstance(document.getElementById('propModal')).hide();
  renderStep();
}

function removeProperty(index) {
  state.draft.properties.splice(index, 1);
  // 编号自动生成，删除后重新排列，保持从 0 开始连续
  state.draft.properties.forEach((p, i) => p.number = i);
  renderStep();
}

// ===== 步骤 2: 服务配置 =====
function methodsBody() {
  const rows = state.draft.methods || [];
  let tbody = '';
  if (rows.length === 0) {
    tbody = `<tr><td colspan="6" class="text-center text-muted py-4">暂无服务，点击右上角「新增服务」添加</td></tr>`;
  } else {
    tbody = rows.map((r, i) => `
      <tr>
        <td>${r.number}</td>
        <td><code>${escapeHtml(r.key)}</code></td>
        <td>${escapeHtml(r.name)}</td>
        <td>${typeBadge(r.type)}</td>
        <td>${escapeHtml(r.desc || '-')}</td>
        <td class="text-end">
          <button class="btn btn-sm btn-outline-primary" onclick="editMethod(${i})"><i class="bi bi-pencil"></i></button>
          <button class="btn btn-sm btn-outline-danger" onclick="removeMethod(${i})"><i class="bi bi-trash"></i></button>
        </td>
      </tr>`).join('');
  }
  return `
    <div class="info-banner">
      <i class="bi bi-info-circle-fill me-2" style="color:var(--primary)"></i>
      <span class="text-muted">服务定义设备可被<span class="fw-semibold">远程调用</span>的方法，调用时将值下发到指定设备属性。</span>
    </div>
    <div class="d-flex justify-content-end mb-2">
      <button class="btn btn-primary btn-sm" data-bs-toggle="modal" data-bs-target="#methodModal" onclick="resetMethodModal()"><i class="bi bi-plus-lg me-1"></i>新增服务</button>
    </div>
    <div class="table-responsive">
      <table class="table table-hover align-middle">
        <thead><tr>
          <th>编号</th><th>标识符</th><th>服务名称</th><th>类型</th><th>描述</th><th class="text-end">操作</th>
        </tr></thead>
        <tbody>${tbody}</tbody>
      </table>
    </div>`;
}

let _editingMethodIndex = -1;
function resetMethodModal() {
  _editingMethodIndex = -1;
  document.getElementById('mt-number').value = nextRowNumber(state.draft.methods);
  document.getElementById('mt-key').value = '';
  document.getElementById('mt-name').value = '';
  document.getElementById('mt-desc').value = '';
  document.getElementById('mt-type').value = 'number';
  document.getElementById('mt-min').value = '0';
  document.getElementById('mt-max').value = '0';
  document.getElementById('mt-desc-list').innerHTML = '';
  onMethodTypeChange();
}

function editMethod(index) {
  const r = state.draft.methods[index];
  _editingMethodIndex = index;
  document.getElementById('mt-number').value = r.number;
  document.getElementById('mt-key').value = r.key || '';
  document.getElementById('mt-name').value = r.name || '';
  document.getElementById('mt-desc').value = r.desc || '';
  document.getElementById('mt-type').value = r.type || 'number';
  document.getElementById('mt-min').value = (r.validation && r.validation.min != null) ? r.validation.min : 0;
  document.getElementById('mt-max').value = (r.validation && r.validation.max != null) ? r.validation.max : 0;
  const dl = document.getElementById('mt-desc-list');
  dl.innerHTML = (r.descriptions && Array.isArray(r.descriptions) ? r.descriptions : []).map((d, i) => statusDescRow(d, i)).join('') || '';
  onMethodTypeChange();
  new bootstrap.Modal(document.getElementById('methodModal')).show();
}

function addMethod() {
  const m = {
    number: parseInt(document.getElementById('mt-number').value) || 0,
    key: document.getElementById('mt-key').value.trim(),
    name: document.getElementById('mt-name').value.trim(),
    desc: document.getElementById('mt-desc').value.trim(),
    type: document.getElementById('mt-type').value,
    validation: {
      min: parseFloat(document.getElementById('mt-min').value) || 0,
      max: parseFloat(document.getElementById('mt-max').value) || 0
    },
    descriptions: collectStatusDesc('mt-desc-list')
  };
  if (!m.name || !m.key) { toast('请填写服务名称和标识符', 'error'); return; }
  if (m.type === 'status' && !m.descriptions.length) { toast('状态类型(status)至少配置一个状态值', 'error'); return; }
  if (m.type === 'status') {
    // 工程值必填且唯一
    const vals = m.descriptions.map(d => d.value);
    if (vals.some(v => v == null || String(v) === '')) { toast('状态类型的工程值必填', 'error'); return; }
    if (new Set(vals.map(String)).size !== vals.length) { toast('状态类型的工程值不能重复', 'error'); return; }
  }
  if (_editingMethodIndex >= 0) state.draft.methods[_editingMethodIndex] = m;
  else state.draft.methods.push(m);
  bootstrap.Modal.getInstance(document.getElementById('methodModal')).hide();
  renderStep();
}

// 服务类型切换：显隐状态值配置区 + 数值类型才显示最小/最大值（默认不添加未知状态行）
function onMethodTypeChange() {
  const type = document.getElementById('mt-type').value;
  toggleStatusSection('mt-type', 'mt-desc-list');
  document.querySelectorAll('.js-mt-num-only').forEach(el => el.classList.toggle('d-none', type !== 'number'));
}

function removeMethod(index) {
  state.draft.methods.splice(index, 1);
  // 编号自动生成，删除后重新排列，保持从 0 开始连续
  state.draft.methods.forEach((m, i) => m.number = i);
  renderStep();
}

// ===== 步骤 3: 告警配置 =====
function eventsBody() {
  const rows = state.draft.events || [];
  let tbody = '';
  if (rows.length === 0) {
    tbody = `<tr><td colspan="7" class="text-center text-muted py-4">暂无告警，点击右上角「新增告警」添加</td></tr>`;
  } else {
    tbody = rows.map((r, i) => {
      const lv = EVENT_LEVELS[r.level] || EVENT_LEVELS[0];
      const tp = EVENT_TYPES.find(t => t.value === r.type) || { name: r.type };
      return `
      <tr>
        <td>${r.number}</td>
        <td>${escapeHtml(r.name)}</td>
        <td><code>${escapeHtml(r.key)}</code></td>
        <td><span class="badge-pill ${lv.cls}">${lv.name}</span></td>
        <td>${escapeHtml(tp.name)}</td>
        <td><code>${r.threshold}</code> / ${r.time}s</td>
        <td class="text-end">
          <button class="btn btn-sm btn-outline-primary" onclick="editEvent(${i})"><i class="bi bi-pencil"></i></button>
          <button class="btn btn-sm btn-outline-danger" onclick="removeEvent(${i})"><i class="bi bi-trash"></i></button>
        </td>
      </tr>`;
    }).join('');
  }
  return `
    <div class="info-banner">
      <i class="bi bi-info-circle-fill me-2" style="color:var(--primary)"></i>
      <span class="text-muted">告警定义设备的<span class="fw-semibold">异常触发规则</span>，当属性值满足阈值条件持续指定时间后触发事件。</span>
    </div>
    <div class="d-flex justify-content-end mb-2">
      <button class="btn btn-primary btn-sm" data-bs-toggle="modal" data-bs-target="#eventModal" onclick="resetEventModal()"><i class="bi bi-plus-lg me-1"></i>新增告警</button>
    </div>
    <div class="table-responsive">
      <table class="table table-hover align-middle">
        <thead><tr>
          <th>编号</th><th>事件名称</th><th>标识符</th><th>级别</th><th>触发类型</th><th>阈值/时长</th><th class="text-end">操作</th>
        </tr></thead>
        <tbody>${tbody}</tbody>
      </table>
    </div>`;
}

let _editingEventIndex = -1;
function resetEventModal() {
  _editingEventIndex = -1;
  document.getElementById('ev-number').value = nextRowNumber(state.draft.events);
  document.getElementById('ev-name').value = '';
  document.getElementById('ev-key').value = '';
  document.getElementById('ev-desc').value = '';
  document.getElementById('ev-level').value = '0';
  document.getElementById('ev-type').value = 'equal';
  document.getElementById('ev-threshold').value = '0';
  document.getElementById('ev-time').value = '0';
}

function editEvent(index) {
  const r = state.draft.events[index];
  _editingEventIndex = index;
  document.getElementById('ev-number').value = r.number;
  document.getElementById('ev-name').value = r.name || '';
  document.getElementById('ev-key').value = r.key || '';
  document.getElementById('ev-desc').value = r.description || '';
  document.getElementById('ev-level').value = r.level != null ? r.level : 0;
  document.getElementById('ev-type').value = r.type || 'equal';
  document.getElementById('ev-threshold').value = r.threshold != null ? r.threshold : 0;
  document.getElementById('ev-time').value = r.time != null ? r.time : 0;
  new bootstrap.Modal(document.getElementById('eventModal')).show();
}

function addEvent() {
  const e = {
    number: parseInt(document.getElementById('ev-number').value) || 0,
    name: document.getElementById('ev-name').value.trim(),
    key: document.getElementById('ev-key').value.trim(),
    description: document.getElementById('ev-desc').value.trim(),
    level: parseInt(document.getElementById('ev-level').value) || 0,
    type: document.getElementById('ev-type').value,
    threshold: parseFloat(document.getElementById('ev-threshold').value) || 0,
    time: parseInt(document.getElementById('ev-time').value) || 0
  };
  if (!e.name || !e.key) { toast('请填写事件名称和标识符', 'error'); return; }
  if (_editingEventIndex >= 0) state.draft.events[_editingEventIndex] = e;
  else state.draft.events.push(e);
  bootstrap.Modal.getInstance(document.getElementById('eventModal')).hide();
  renderStep();
}

function removeEvent(index) {
  state.draft.events.splice(index, 1);
  // 编号自动生成，删除后重新排列，保持从 0 开始连续
  state.draft.events.forEach((e, i) => e.number = i);
  renderStep();
}

// ===== 步骤 4: 预览 =====
function previewBody() {
  return `
    <div class="info-banner">
      <i class="bi bi-info-circle-fill me-2" style="color:var(--primary)"></i>
      <span class="text-muted">请确认物模型配置信息。可<span class="fw-semibold">导出</span>为 JSON 文件，或点击<span class="fw-semibold">保存</span>写入 templats 目录。</span>
    </div>
    <div class="summary-grid">
      <div class="summary-card"><div class="val" id="pv-prop">0</div><div class="lbl">属性数量</div></div>
      <div class="summary-card"><div class="val" id="pv-mtd">0</div><div class="lbl">服务数量</div></div>
      <div class="summary-card"><div class="val" id="pv-evt">0</div><div class="lbl">告警数量</div></div>
    </div>
    <div class="form-section-divider"><span><i class="bi bi-file-code me-1"></i>JSON 配置预览</span></div>
    <pre class="code-preview" id="pv-json"></pre>
    <div class="d-flex justify-content-end gap-2 mt-3">
      <button class="btn btn-outline-secondary" onclick="exportDraft()"><i class="bi bi-download me-1"></i>导出 JSON</button>
      <button class="btn btn-success" onclick="saveDraft()"><i class="bi bi-database-check me-1"></i>保存模板</button>
    </div>`;
}

function renderPreview() {
  const d = state.draft;
  document.getElementById('pv-prop').textContent = (d.properties || []).length;
  document.getElementById('pv-mtd').textContent = (d.methods || []).length;
  document.getElementById('pv-evt').textContent = (d.events || []).length;
  document.getElementById('pv-json').textContent = JSON.stringify(d, null, 4);
}

function exportDraft() {
  const filename = (state.draft.code || state.draft.name || 'template') + '.json';
  downloadFile(filename, JSON.stringify(state.draft, null, 4), 'application/json');
}

async function saveDraft() {
  if (!state.draft.name || !state.draft.code) {
    toast('请先在档案信息中填写模板名称和编码', 'error');
    state.currentStep = 0;
    renderStepper(); renderStep();
    return;
  }
  try {
    const result = await TemplatesAPI.save(state.draft);
    const synced = result && result.syncedDevices;
    if (synced > 0) {
      toast(`模板已${state.isEditing ? '更新' : '保存'}，并同步到 ${synced} 个设备实例`);
    } else if (result && result.syncError) {
      toast(`模板已${state.isEditing ? '更新' : '保存'}，但同步设备失败：${result.syncError}`, 'error');
    } else {
      toast(state.isEditing ? '模板已更新' : '模板已保存');
    }
    await loadTemplates();
    switchSection('templates');
  } catch (e) {
    toast(e.message, 'error');
  }
}

// ===== 状态值编辑组件（属性/服务 status 类型）：状态值 / 状态标签 / 状态名称 / 工程值 =====
function statusDescRow(d, i) {
  return `
    <div class="status-row">
      <input type="number" class="form-control st-enum" placeholder="如：0" value="${d.enum != null ? d.enum : ''}">
      <input type="text" class="form-control st-key" placeholder="如：online" value="${escapeHtml(d.key || '')}">
      <input type="text" class="form-control st-name" placeholder="如：在线" value="${escapeHtml(d.name || '')}">
      <input type="number" class="form-control st-value" placeholder="如：1" value="${d.value != null ? d.value : ''}">
      <button class="btn btn-outline-danger" onclick="removeRow(this)">×</button>
    </div>`;
}

function addStatusRow(listId, d) {
  d = d || {};
  const list = document.getElementById(listId);
  const div = document.createElement('div');
  div.className = 'status-row';
  div.innerHTML = `
    <input type="number" class="form-control st-enum" placeholder="如：0" value="${d.enum != null ? d.enum : ''}">
    <input type="text" class="form-control st-key" placeholder="如：online" value="${escapeHtml(d.key || '')}">
    <input type="text" class="form-control st-name" placeholder="如：在线" value="${escapeHtml(d.name || '')}">
    <input type="number" class="form-control st-value" placeholder="如：1" value="${d.value != null ? d.value : ''}">
    <button class="btn btn-outline-danger" onclick="removeRow(this)">×</button>`;
  list.appendChild(div);
}

// 属性类型切换：显隐状态值配置区 + 补默认未知状态行
function onPropTypeChange() {
  toggleStatusSection('prop-type', 'prop-desc-list');
  // 切换到状态类型且状态值列表为空时，自动添加默认未知状态行（状态值 -999，工程值留空）
  if (document.getElementById('prop-type').value === 'status' &&
      document.querySelectorAll('#prop-desc-list .status-row').length === 0) {
    addStatusRow('prop-desc-list', { enum: -999, key: 'unknown', name: '未知', value: null });
  }
}

// 收集状态值列表：四项中任一项非空即保留该行；工程值留空存 null
function collectStatusDesc(listId) {
  const out = [];
  document.querySelectorAll('#' + listId + ' .status-row').forEach(row => {
    const enumValue = row.querySelector('.st-enum').value.trim();
    const key = row.querySelector('.st-key').value.trim();
    const name = row.querySelector('.st-name').value.trim();
    const value = row.querySelector('.st-value').value.trim();
    if (enumValue || key || name || value) {
      out.push({
        enum: enumValue !== '' ? parseInt(enumValue) : 0,
        key: key,
        name: name,
        value: value !== '' ? parseInt(value) : null
      });
    }
  });
  return out;
}

function parseValue(v) {
  if (v === '') return 0;
  const n = Number(v);
  return isNaN(n) ? v : n;
}

function parseDefault(v, type) {
  // 默认值允许为空：留空表示不设置默认值
  if (v == null || v.trim() === '') return null;
  if (type === 'number') {
    const n = Number(v);
    return isNaN(n) ? 0 : n;
  }
  return parseValue(v);
}

// 切换状态值配置区显隐（status 类型显示）
function toggleStatusSection(typeId, descId) {
  const type = document.getElementById(typeId).value;
  const desc = document.getElementById(descId);
  const wrap = desc.closest('.desc-section-wrap');
  if (wrap) wrap.style.display = (type === 'status') ? '' : 'none';
}

function removeRow(btn) { btn.closest('.status-row').remove(); }

// ===== 向导导航 =====
// 穿越：点击顶部步骤导航直接跳转到对应步骤
function goToStep(index) {
  if (index < 0 || index >= WIZARD_STEPS.length) return;
  if (index === state.currentStep) return;
  state.currentStep = index;
  renderStepper();
  renderStep();
}

function nextStep() {
  if (state.currentStep === 0 && !validateProfile()) {
    toast('请填写必填的档案信息（名称、编码）', 'error');
    return;
  }
  if (state.currentStep < WIZARD_STEPS.length - 1) {
    state.currentStep++;
    renderStepper(); renderStep();
  }
}

function prevStep() {
  if (state.currentStep > 0) {
    state.currentStep--;
    renderStepper(); renderStep();
  }
}
