// helpers.js — 通用工具函数
// 遵循 frontend-backend-collaboration.md §4.3

// HTML 转义，防止 XSS
function escapeHtml(s) {
  return String(s == null ? '' : s).replace(/[&<>"']/g, c => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'
  }[c]));
}

// 区块切换：隐藏所有 section，显示目标 section，同步侧栏 active
function switchSection(key) {
  document.querySelectorAll('.app-section').forEach(s => s.classList.add('d-none'));
  const target = document.getElementById('section-' + key);
  if (target) target.classList.remove('d-none');
  document.querySelectorAll('.sidebar-item').forEach(i => i.classList.remove('active'));
  const nav = document.getElementById('nav-' + key);
  if (nav) nav.classList.add('active');

  // 按区块触发渲染
  if (key === 'templates') renderTemplateList();
}

// 触发文件下载（用于导出 JSON）
function downloadFile(filename, content, mime) {
  const blob = new Blob([content], { type: mime || 'application/json' });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url; a.download = filename;
  document.body.appendChild(a); a.click();
  document.body.removeChild(a);
  URL.revokeObjectURL(url);
}

// 读取用户选择的文本文件内容（用于导入）
function readFileText(file) {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(reader.result);
    reader.onerror = () => reject(reader.error);
    reader.readAsText(file);
  });
}

// 简易 toast 提示（基于 alert 的轻量替代）
function toast(msg, type) {
  // 使用浏览器原生 alert 避免引入额外组件
  if (type === 'error') {
    alert('错误：' + msg);
  } else {
    alert(msg);
  }
}

// 校验字符串非空
function notEmpty(v) { return v != null && String(v).trim() !== ''; }
