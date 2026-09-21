// main.js — 启动入口
// 遵循 frontend-backend-collaboration.md §4.5

const AUTH_KEY = 'tm_auth';

function init() {
  bindLogin();
  // 同一浏览器会话内已登录（如点击刷新）时跳过登录界面，直接进入工作台
  if (sessionStorage.getItem(AUTH_KEY) === '1') {
    enterApp();
    return;
  }
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
  if (u === LOGIN_USER && p === LOGIN_PASS) {
    sessionStorage.setItem(AUTH_KEY, '1');
    enterApp();
  } else {
    document.getElementById('login-err').classList.remove('d-none');
  }
}

// 进入工作台：隐藏登录遮罩、显示应用外壳并加载首页数据
function enterApp() {
  document.getElementById('login-err').classList.add('d-none');
  document.getElementById('login-overlay').classList.add('d-none');
  document.getElementById('app-shell').classList.remove('d-none');
  // 加载模板列表后渲染
  loadTemplates().then(() => switchSection('templates'));
}

document.addEventListener('DOMContentLoaded', init);
