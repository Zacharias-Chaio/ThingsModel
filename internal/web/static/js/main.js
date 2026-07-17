// main.js — 启动入口
// 遵循 frontend-backend-collaboration.md §4.5

function init() {
  bindLogin();
  document.getElementById('login-user').focus();
}

// 登录处理
function bindLogin() {
  document.getElementById('login-btn').addEventListener('click', doLogin);
  ['login-user', 'login-pass'].forEach(id => {
    document.getElementById(id).addEventListener('keydown', e => { if (e.key === 'Enter') doLogin(); });
  });
}

function doLogin() {
  const u = document.getElementById('login-user').value.trim();
  const p = document.getElementById('login-pass').value.trim();
  const err = document.getElementById('login-err');
  if (u === LOGIN_USER && p === LOGIN_PASS) {
    err.classList.add('d-none');
    document.getElementById('login-overlay').classList.add('d-none');
    document.getElementById('app-shell').classList.remove('d-none');
    // 加载模板列表后渲染
    loadTemplates().then(() => switchSection('templates'));
  } else {
    err.classList.remove('d-none');
  }
}

document.addEventListener('DOMContentLoaded', init);
