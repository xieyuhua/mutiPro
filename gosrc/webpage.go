package main

// webPageHTML 是内嵌的单页管理界面。
//
// 全部内联（HTML/CSS/JS 都在一个文件里），不依赖任何 CDN 或
// 外部资源：局域网内的机器可能没有外网，页面必须离线可用。
const webPageHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>MultiPro 管理器</title>
<style>
  :root {
    --bg: #f1f5f9;
    --card: #ffffff;
    --border: #e2e8f0;
    --text: #0f172a;
    --muted: #64748b;
    --accent: #4f46e5;
    --green: #16a34a;
    --red: #dc2626;
    --gray: #94a3b8;
  }
  * { box-sizing: border-box; }
  body {
    margin: 0;
    font-family: "Microsoft YaHei UI", -apple-system, "Segoe UI", sans-serif;
    background: var(--bg);
    color: var(--text);
    font-size: 14px;
  }
  .hidden { display: none !important; }

  /* 登录页 */
  #login {
    max-width: 340px;
    margin: 18vh auto;
    background: var(--card);
    padding: 28px;
    border-radius: 12px;
    box-shadow: 0 4px 20px rgba(15, 23, 42, .08);
  }
  #login h1 { margin: 0 0 6px; font-size: 20px; }
  #login p { margin: 0 0 20px; color: var(--muted); font-size: 13px; }
  input[type=password] {
    width: 100%;
    padding: 10px 12px;
    border: 1px solid var(--border);
    border-radius: 8px;
    font-size: 15px;
    margin-bottom: 12px;
  }
  input[type=password]:focus { outline: 2px solid var(--accent); border-color: transparent; }

  button {
    border: 0;
    border-radius: 8px;
    padding: 9px 16px;
    font-size: 13px;
    cursor: pointer;
    font-family: inherit;
    background: #e2e8f0;
    color: var(--text);
  }
  button:hover { filter: brightness(.96); }
  button:disabled { opacity: .45; cursor: not-allowed; }
  button.primary { background: var(--accent); color: #fff; width: 100%; }
  button.start { background: var(--green); color: #fff; }
  button.stop { background: var(--red); color: #fff; }
  button.ghost { background: transparent; border: 1px solid var(--border); }

  /* 主界面 */
  header {
    background: var(--card);
    border-bottom: 1px solid var(--border);
    padding: 12px 16px;
    display: flex;
    align-items: center;
    gap: 12px;
    position: sticky;
    top: 0;
    z-index: 10;
  }
  header h1 { font-size: 16px; margin: 0; font-weight: 600; }
  header .host { color: var(--muted); font-size: 12px; }
  header .spacer { flex: 1; }

  main { padding: 14px; max-width: 1100px; margin: 0 auto; }

  .card {
    background: var(--card);
    border: 1px solid var(--border);
    border-radius: 10px;
    padding: 14px;
    margin-bottom: 10px;
  }
  .row { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
  .name { font-weight: 600; font-size: 15px; }
  .dot { width: 8px; height: 8px; border-radius: 50%; background: var(--gray); }
  .dot.on { background: var(--green); }
  .dot.busy { background: #d97706; }
  .meta { color: var(--muted); font-size: 12px; }
  .actions { margin-left: auto; display: flex; gap: 6px; }
  .actions button { padding: 7px 14px; font-size: 12px; }

  #logPanel { margin-top: 14px; }
  #logPanel pre {
    background: #0f172a;
    color: #e2e8f0;
    padding: 12px;
    border-radius: 8px;
    font-family: Consolas, "Courier New", monospace;
    font-size: 12px;
    line-height: 1.5;
    max-height: 58vh;
    overflow: auto;
    white-space: pre-wrap;
    word-break: break-all;
    margin: 0;
  }
  .empty { color: var(--muted); text-align: center; padding: 30px 0; }
  .toast {
    position: fixed;
    left: 50%;
    bottom: 28px;
    transform: translateX(-50%);
    background: #0f172a;
    color: #fff;
    padding: 10px 18px;
    border-radius: 8px;
    font-size: 13px;
    opacity: 0;
    transition: opacity .2s;
    pointer-events: none;
    max-width: 86vw;
  }
  .toast.show { opacity: .94; }
  .toast.error { background: #b91c1c; }

  @media (max-width: 560px) {
    main { padding: 10px; }
    .card { padding: 12px; }
    .actions { width: 100%; margin-left: 0; margin-top: 10px; }
    .actions button { flex: 1; }
    header h1 { font-size: 15px; }
  }
</style>
</head>
<body>

<!-- 登录 -->
<div id="login">
  <h1>MultiPro 管理器</h1>
  <p>请输入访问密码</p>
  <input type="password" id="pwd" placeholder="密码" autocomplete="current-password">
  <button class="primary" id="loginBtn">登录</button>
</div>

<!-- 主界面 -->
<div id="app" class="hidden">
  <header>
    <h1>MultiPro</h1>
    <span class="host" id="hostLabel"></span>
    <span class="spacer"></span>
    <button class="ghost" id="startAllBtn">全部启动</button>
    <button class="ghost" id="stopAllBtn">全部停止</button>
    <button class="ghost" id="logoutBtn">退出</button>
  </header>
  <main>
    <div id="list"></div>
    <div id="logPanel" class="card hidden">
      <div class="row" style="margin-bottom:10px">
        <strong id="logTitle">运行日志</strong>
        <span class="meta" id="logMeta"></span>
        <span class="spacer" style="flex:1"></span>
        <button class="ghost" id="refreshLogBtn">刷新</button>
        <button class="ghost" id="closeLogBtn">关闭</button>
      </div>
      <pre id="logContent"></pre>
    </div>
  </main>
</div>

<div class="toast" id="toast"></div>

<script>
(function () {
  var state = { programs: [], openId: null, timer: null, logTimer: null };

  function $(id) { return document.getElementById(id); }

  function toast(message, isError) {
    var el = $('toast');
    el.textContent = message;
    el.className = 'toast show' + (isError ? ' error' : '');
    clearTimeout(el._timer);
    el._timer = setTimeout(function () { el.className = 'toast'; }, 2600);
  }

  // 统一请求封装：401 时回到登录页。
  function api(path, options) {
    options = options || {};
    options.headers = options.headers || {};
    if (options.body) options.headers['Content-Type'] = 'application/json';
    return fetch(path, options).then(function (resp) {
      if (resp.status === 401) {
        showLogin();
        throw new Error('未登录');
      }
      return resp.json().catch(function () { return {}; }).then(function (data) {
        if (!resp.ok) throw new Error(data.error || ('HTTP ' + resp.status));
        return data;
      });
    });
  }

  function showLogin() {
    $('app').classList.add('hidden');
    $('login').classList.remove('hidden');
    stopTimers();
  }

  function showApp() {
    $('login').classList.add('hidden');
    $('app').classList.remove('hidden');
    refresh();
    startTimers();
  }

  function startTimers() {
    stopTimers();
    // 列表每 3 秒刷新，日志页每 2 秒刷新。
    state.timer = setInterval(refresh, 3000);
    state.logTimer = setInterval(refreshLog, 2000);
  }

  function stopTimers() {
    if (state.timer) clearInterval(state.timer);
    if (state.logTimer) clearInterval(state.logTimer);
    state.timer = state.logTimer = null;
  }

  function refresh() {
    api('/api/programs').then(function (data) {
      state.programs = data.programs || [];
      $('hostLabel').textContent = data.hostname ? ('主机 ' + data.hostname) : '';
      render();
    }).catch(function (err) {
      if (err.message !== '未登录') toast(err.message, true);
    });
  }

  function render() {
    var list = $('list');
    if (!state.programs.length) {
      list.innerHTML = '<div class="card empty">还没有被管理的程序</div>';
      return;
    }
    var html = '';
    state.programs.forEach(function (p) {
      var dotClass = p.running ? 'on' : '';
      var meta = [];
      if (p.pid) meta.push('PID ' + p.pid);
      if (p.uptime) meta.push('运行 ' + p.uptime);
      if (p.type) meta.push(p.type);
      if (p.schedule) meta.push('定时 ' + p.schedule);

      html += '<div class="card">' +
        '<div class="row">' +
          '<span class="dot ' + dotClass + '"></span>' +
          '<span class="name">' + esc(p.name) + '</span>' +
          '<span class="meta">' + esc(p.state) + '</span>' +
          '<div class="actions">' +
            '<button class="ghost" data-act="log" data-id="' + esc(p.id) + '">日志</button>' +
            '<button class="start" data-act="start" data-id="' + esc(p.id) + '"' +
              (p.running ? ' disabled' : '') + '>启动</button>' +
            '<button class="stop" data-act="stop" data-id="' + esc(p.id) + '"' +
              (p.running ? '' : ' disabled') + '>停止</button>' +
            '<button class="ghost" data-act="restart" data-id="' + esc(p.id) + '">重启</button>' +
          '</div>' +
        '</div>' +
        (meta.length ? '<div class="meta" style="margin-top:6px">' + esc(meta.join(' · ')) + '</div>' : '') +
      '</div>';
    });
    list.innerHTML = html;
  }

  function esc(text) {
    return String(text == null ? '' : text)
      .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;').replace(/'/g, '&#39;');
  }

  function act(action, id) {
    api('/api/action', {
      method: 'POST',
      body: JSON.stringify({ action: action, id: id || '' })
    }).then(function (data) {
      toast(data.message || '已提交');
      refresh();
    }).catch(function (err) { toast(err.message, true); });
  }

  function openLog(id, name) {
    state.openId = id;
    $('logPanel').classList.remove('hidden');
    $('logTitle').textContent = name + ' 的日志';
    $('logContent').textContent = '加载中…';
    refreshLog();
  }

  function refreshLog() {
    if (!state.openId) return;
    api('/api/log?id=' + encodeURIComponent(state.openId) + '&tail=300')
      .then(function (data) {
        var pre = $('logContent');
        // 用户向上翻看时不强行滚动到底部。
        var atBottom = pre.scrollTop + pre.clientHeight >= pre.scrollHeight - 30;
        pre.textContent = data.log || '（暂无日志）';
        $('logMeta').textContent = data.name || '';
        if (atBottom) pre.scrollTop = pre.scrollHeight;
      }).catch(function (err) {
        if (err.message !== '未登录') $('logContent').textContent = '读取失败：' + err.message;
      });
  }

  // ---- 事件绑定 ----

  $('loginBtn').addEventListener('click', function () {
    var pwd = $('pwd').value;
    if (!pwd) { toast('请输入密码', true); return; }
    api('/api/login', { method: 'POST', body: JSON.stringify({ password: pwd }) })
      .then(function () { $('pwd').value = ''; showApp(); })
      .catch(function (err) { toast(err.message, true); });
  });

  $('pwd').addEventListener('keydown', function (e) {
    if (e.key === 'Enter') $('loginBtn').click();
  });

  $('logoutBtn').addEventListener('click', function () {
    api('/api/logout', { method: 'POST' }).then(showLogin).catch(showLogin);
  });

  $('startAllBtn').addEventListener('click', function () { act('startAll'); });
  $('stopAllBtn').addEventListener('click', function () { act('stopAll'); });

  $('refreshLogBtn').addEventListener('click', refreshLog);
  $('closeLogBtn').addEventListener('click', function () {
    state.openId = null;
    $('logPanel').classList.add('hidden');
  });

  // 列表用事件委托，避免每次重绘都重新绑定。
  $('list').addEventListener('click', function (e) {
    var btn = e.target.closest('button[data-act]');
    if (!btn) return;
    var action = btn.getAttribute('data-act');
    var id = btn.getAttribute('data-id');
    if (action === 'log') {
      var item = state.programs.filter(function (p) { return p.id === id; })[0];
      openLog(id, item ? item.name : '');
      return;
    }
    act(action, id);
  });

  // 页面打开时先试探会话是否仍然有效。
  api('/api/programs').then(showApp).catch(showLogin);
})();
</script>
</body>
</html>
`
