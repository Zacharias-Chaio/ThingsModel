// constants.js — 常量定义与全局 state（单一数据源）
// 遵循 frontend-backend-collaboration.md §4.2

// 登录凭据（前端静态校验）
const LOGIN_USER = 'admin';
const LOGIN_PASS = '666';

// 向导步骤定义（档案信息 → 属性配置 → 服务配置 → 告警配置 → 预览）
const WIZARD_STEPS = [
  { key: 'profile',     title: '档案信息配置', sub: '填写物模型模板的基础档案信息', icon: 'bi-card-text' },
  { key: 'properties',  title: '属性配置',     sub: '定义设备的属性数据点（数值/枚举）', icon: 'bi-sliders' },
  { key: 'methods',     title: '服务配置',     sub: '定义设备可被调用的方法/服务',     icon: 'bi-gear' },
  { key: 'events',      title: '告警配置',     sub: '定义设备的告警/事件触发规则',    icon: 'bi-bell' },
  { key: 'preview',     title: '预览',         sub: '确认物模型配置并导出保存',       icon: 'bi-eye' }
];

// 属性聚合方法枚举（模板说明.md）
const BINDING_METHODS = ['EPT', 'SUM', 'AVG', 'MIN', 'MAX', 'AND', 'OR', 'NOT'];

// 事件告警级别
const EVENT_LEVELS = [
  { value: 0, name: '提示', cls: 'alarm-level-0' },
  { value: 1, name: '一般', cls: 'alarm-level-1' },
  { value: 2, name: '严重', cls: 'alarm-level-2' },
  { value: 3, name: '紧急', cls: 'alarm-level-3' }
];

// 事件触发类型
const EVENT_TYPES = [
  { value: 'equal', name: '等值触发' },
  { value: 'upper', name: '上阈值触发' },
  { value: 'lower', name: '下阈值触发' }
];

// 全局 state（唯一数据源）
const state = {
  templates: [],            // 模板列表
  currentStep: 0,           // 当前向导步骤索引
  isEditing: false,         // 是否编辑现有模板
  draft: emptyDraft(),      // 向导弹稿
};

// 创建空白草稿
function emptyDraft() {
  return {
    name: '',
    code: '',
    category: '',
    version: '1.0.0',
    description: '',
    properties: [],
    methods: [],
    events: []
  };
}
