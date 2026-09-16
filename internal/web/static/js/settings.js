// settings.js - Platform configuration and runtime maintenance.

const SETTINGS_CARDS = [
  { icon: 'journal-text', title: '日志配置', subtitle: '终端与文件日志输出', fields: [
    { path: 'logger.level', label: '日志级别', type: 'select', options: [['debug', 'Debug'], ['info', 'Info'], ['warn', 'Warn'], ['error', 'Error']] },
    { path: 'logger.console', label: '终端输出', type: 'boolean' },
    { path: 'logger.file', label: '文件输出', type: 'boolean' },
    { path: 'logger.fileConfig.filename', label: '日志文件路径' },
    { path: 'logger.fileConfig.maxSize', label: '单文件上限 (MB)', type: 'number', min: 1 },
    { path: 'logger.fileConfig.maxBackups', label: '历史文件份数', type: 'number', min: 0 },
    { path: 'logger.fileConfig.maxAge', label: '保留天数', type: 'number', min: 0 },
    { path: 'logger.fileConfig.compress', label: '压缩归档', type: 'boolean' },
  ] },
  { icon: 'broadcast-pin', title: '数据订阅', subtitle: '订阅网关遥测输入', fields: [
    { path: 'subscriber.enabled', label: '启用订阅', type: 'boolean' },
    { path: 'subscriber.url', label: '服务地址' },
    { path: 'subscriber.name', label: '连接名称' },
    { path: 'subscriber.inputSubjectPrefix', label: '输入主题前缀', hint: '网关数据主题：{前缀}.{gatewayId}.data' },
    { path: 'subscriber.connectTimeout', label: '连接超时 (ms)', type: 'number', min: 0 },
    { path: 'subscriber.reconnectWait', label: '重连间隔 (ms)', type: 'number', min: 0 },
    { path: 'subscriber.maxReconnects', label: '最大重连次数', type: 'number', min: -1 },
    { path: 'subscriber.retryOnFailedConnect', label: '失败后重试', type: 'boolean' },
    { path: 'subscriber.pingInterval', label: '心跳间隔 (ms)', type: 'number', min: 0 },
    { path: 'subscriber.maxPingsOut', label: '最大未响应心跳数', type: 'number', min: 0 },
    { path: 'staleAfter', label: '数据过期时长 (ms)', type: 'number', min: 0, hint: '0 表示不因时间标记数据过期。' },
  ] },
  { icon: 'send', title: '内容发布', subtitle: '归一化数据与业务消息扇出', fields: [
    { path: 'publisher.enabled', label: '启用发布', type: 'boolean' },
    { path: 'publisher.url', label: '服务地址' },
    { path: 'publisher.name', label: '连接名称' },
    { path: 'publisher.queueSize', label: '发布队列长度', type: 'number', min: 1 },
    { path: 'publisher.connectTimeout', label: '连接超时 (ms)', type: 'number', min: 0 },
    { path: 'publisher.reconnectWait', label: '重连间隔 (ms)', type: 'number', min: 0 },
    { path: 'publisher.maxReconnects', label: '最大重连次数', type: 'number', min: -1 },
    { path: 'publisher.retryOnFailedConnect', label: '失败后重试', type: 'boolean' },
    { path: 'publisher.reconnectBufSize', label: '断连缓冲 (bytes)', type: 'number', min: 0 },
    { path: 'publisher.pingInterval', label: '心跳间隔 (ms)', type: 'number', min: 0 },
    { path: 'publisher.maxPingsOut', label: '最大未响应心跳数', type: 'number', min: 0 },
  ] },
];

function settingsFieldID(path) { return 'setting-' + path.replaceAll('.', '-'); }
function settingsGet(object, path) { return path.split('.').reduce((value, key) => value && value[key], object); }
function settingsSet(object, path, value) {
  const keys = path.split('.');
  const last = keys.pop();
  const target = keys.reduce((value, key) => value[key] || (value[key] = {}), object);
  target[last] = value;
}

async function loadPlatformSettings() {
  try {
    state.settings = await SettingsAPI.get();
  } catch (error) {
    state.settings = null;
    toast(error.message, 'error');
  }
}

async function loadSystemInfo() {
  try {
    state.systemInfo = await SettingsAPI.system();
  } catch (error) {
    state.systemInfo = null;
  }
}

function renderSettingsField(field, settings) {
  const id = settingsFieldID(field.path);
  const value = settingsGet(settings, field.path);
  const hint = field.hint ? `<div class="form-text">${escapeHtml(field.hint)}</div>` : '';
  if (field.type === 'boolean') return `<div class="settings-field"><label for="${id}">${escapeHtml(field.label)}</label><div class="settings-switch"><span>${hint}</span><div class="form-check form-switch m-0"><input id="${id}" class="form-check-input" type="checkbox"${value ? ' checked' : ''}></div></div></div>`;
  if (field.type === 'select') return `<div class="settings-field"><label for="${id}">${escapeHtml(field.label)}</label><select id="${id}" class="form-select">${field.options.map(([optionValue, optionLabel]) => `<option value="${escapeHtml(optionValue)}"${optionValue === value ? ' selected' : ''}>${escapeHtml(optionLabel)}</option>`).join('')}</select>${hint}</div>`;
  const type = field.type === 'number' ? 'number' : 'text';
  const min = field.min !== undefined ? ` min="${field.min}"` : '';
  return `<div class="settings-field"><label for="${id}">${escapeHtml(field.label)}</label><input id="${id}" type="${type}" class="form-control" value="${escapeHtml(value)}"${min}>${hint}</div>`;
}

function settingsCard(icon, title, subtitle, body) {
  return `<article class="settings-card"><div class="settings-card-head"><i class="bi bi-${icon}"></i><div><div class="settings-card-title">${escapeHtml(title)}</div><div class="settings-card-sub">${escapeHtml(subtitle)}</div></div></div>${body}</article>`;
}

function subscriptionCard(subscriptions) {
  const rows = subscriptions.map(subscription => subscriptionRow(subscription.gatewayId)).join('');
  return settingsCard('hdd-network', '设备订阅', '填写上游 Gateway ID，订阅该网关全部采集设备与属性', `<div class="settings-subscription-rows">${rows}</div><button class="btn btn-outline-secondary btn-sm mt-2" type="button" onclick="addSubscriptionRow()"><i class="bi bi-plus-lg"></i> 添加网关</button>`);
}

function subscriptionRow(gatewayID = '') {
  return `<div class="settings-subscription-row"><input class="form-control form-control-sm subscription-gateway-id" value="${escapeHtml(gatewayID)}" placeholder="上游 Gateway ID，如 gw-001"><button class="btn btn-outline-danger btn-sm" type="button" onclick="this.closest('.settings-subscription-row').remove()" title="删除订阅"><i class="bi bi-trash"></i></button></div>`;
}

function addSubscriptionRow() {
  const target = document.querySelector('.settings-subscription-rows');
  if (target) target.insertAdjacentHTML('beforeend', subscriptionRow());
}

function softwareInfoCard() {
  const info = state.systemInfo;
  return settingsCard('info-circle', '软件信息', '当前运行环境', `<dl class="settings-static"><div><dt>操作系统</dt><dd>${escapeHtml(info ? info.operatingSystem : '不可用')}</dd></div><div><dt>系统时间</dt><dd>${escapeHtml(info ? new Date(info.systemTime).toLocaleString() : '不可用')}</dd></div><div><dt>ThingsModel 版本</dt><dd>${escapeHtml(info ? info.thingsModelVersion : '不可用')}</dd></div></dl>`);
}

function restartCard() {
  return settingsCard('arrow-repeat', '软件设置', '运行时维护操作', `<div class="settings-restart"><p>重建消息客户端连接并清空接收缓存，HTTP 服务保持可用。</p><button id="software-restart" class="btn btn-outline-danger w-100" type="button" onclick="restartSoftware()"><i class="bi bi-arrow-clockwise me-1"></i>软件重启</button></div>`);
}

function renderSettings() {
  const target = document.getElementById('settings-cards');
  const save = document.getElementById('settings-save');
  if (!target) return;
  if (!state.settings) {
    target.innerHTML = '<div class="empty-state"><i class="bi bi-exclamation-circle"></i><div>无法读取平台设置。</div></div>';
    if (save) save.disabled = true;
    return;
  }
  if (save) save.disabled = false;
  const cards = SETTINGS_CARDS.map(card => settingsCard(card.icon, card.title, card.subtitle, `<div class="settings-fields">${card.fields.map(field => renderSettingsField(field, state.settings)).join('')}</div>`));
  target.innerHTML = [
    softwareInfoCard() + subscriptionCard(state.settings.deviceSubscriptions || []) + cards[0] + restartCard(),
    cards[2],
    cards[1],
  ].map(column => `<div class="settings-column">${column}</div>`).join('');
}

function collectSubscriptions() {
  const values = [...document.querySelectorAll('.subscription-gateway-id')].map(input => input.value.trim()).filter(Boolean);
  if (new Set(values).size !== values.length) throw new Error('上游 Gateway ID 不能重复');
  return values.map(gatewayId => ({ gatewayId }));
}

async function saveSettings() {
  try {
    if (!state.settings) throw new Error('平台设置尚未加载');
    const settings = JSON.parse(JSON.stringify(state.settings));
    SETTINGS_CARDS.forEach(card => card.fields.forEach(field => {
      const input = document.getElementById(settingsFieldID(field.path));
      let value = field.type === 'boolean' ? input.checked : input.value.trim();
      if (field.type === 'number') {
        value = Number(value);
        if (!Number.isFinite(value)) throw new Error(`${field.label} 必须是数字`);
      }
      settingsSet(settings, field.path, value);
    }));
    settings.deviceSubscriptions = collectSubscriptions();
    state.settings = await SettingsAPI.save(settings);
    renderSettings();
    toast('设置已保存，日志与消息客户端已分别热加载。');
  } catch (error) {
    toast('保存设置失败：' + error.message, 'error');
  }
}

async function restartSoftware() {
  if (!confirm('确认软件重启？消息订阅会短暂中断，接收缓存将被清空。')) return;
  const button = document.getElementById('software-restart');
  button.disabled = true;
  button.innerHTML = '<span class="spinner-border spinner-border-sm me-1" aria-hidden="true"></span>正在重启';
  try {
    await SettingsAPI.restart();
    toast('运行时正在重启。');
    setTimeout(() => { button.disabled = false; button.innerHTML = '<i class="bi bi-arrow-clockwise me-1"></i>软件重启'; }, 800);
  } catch (error) {
    button.disabled = false;
    button.innerHTML = '<i class="bi bi-arrow-clockwise me-1"></i>软件重启';
    toast('软件重启失败：' + error.message, 'error');
  }
}
