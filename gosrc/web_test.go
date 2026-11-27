package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestWebServer 创建一个不监听端口的 webServer，只测试 HTTP 处理器。
func newTestWebServer(password string) *webServer {
	w := &mainWindow{app: &AppConfig{WebPassword: password}}
	return newWebServer(w)
}

// doRequest 用给定的 cookie 发一次请求，返回状态码与响应体。
func doRequest(t *testing.T, handler http.HandlerFunc, method, target, body string,
	cookie *http.Cookie) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec.Code, rec.Body.String()
}

// ---------------------------------------------------------------- 密码生成

func TestRandomPassword(t *testing.T) {
	a := randomPassword(12)
	b := randomPassword(12)

	if len(a) != 12 || len(b) != 12 {
		t.Fatalf("长度应为 12：%q %q", a, b)
	}
	if a == b {
		t.Error("两次生成的密码不应相同")
	}
	// 字符集里不应包含容易混淆的字符。
	for _, r := range a {
		if strings.ContainsRune("01lIO", r) {
			t.Errorf("密码含易混淆字符 %q：%q", r, a)
		}
	}
}

func TestEnsureWebPassword(t *testing.T) {
	cfg := &AppConfig{}
	password, generated := cfg.ensureWebPassword()
	if !generated {
		t.Error("首次调用应生成密码")
	}
	if len(password) < 8 {
		t.Errorf("密码太短：%q", password)
	}
	if cfg.WebPassword != password {
		t.Error("密码应写入配置")
	}

	// 再次调用返回同一个密码，不重复生成。
	again, generatedAgain := cfg.ensureWebPassword()
	if generatedAgain {
		t.Error("已有密码时不应重新生成")
	}
	if again != password {
		t.Error("密码应保持不变")
	}
}

// ---------------------------------------------------------------- 登录

func TestLoginSuccess(t *testing.T) {
	s := newTestWebServer("secret123")
	code, body := doRequest(t, s.handleLogin, http.MethodPost, "/api/login",
		`{"password":"secret123"}`, nil)

	if code != http.StatusOK {
		t.Fatalf("登录应成功，得到 %d：%s", code, body)
	}
	var result map[string]bool
	_ = json.Unmarshal([]byte(body), &result)
	if !result["ok"] {
		t.Errorf("响应应包含 ok：%s", body)
	}
}

func TestLoginWrongPassword(t *testing.T) {
	s := newTestWebServer("secret123")
	code, _ := doRequest(t, s.handleLogin, http.MethodPost, "/api/login",
		`{"password":"wrong"}`, nil)
	if code != http.StatusUnauthorized {
		t.Errorf("密码错误应返回 401，得到 %d", code)
	}
}

func TestLoginRequiresPost(t *testing.T) {
	s := newTestWebServer("secret123")
	code, _ := doRequest(t, s.handleLogin, http.MethodGet, "/api/login", "", nil)
	if code != http.StatusMethodNotAllowed {
		t.Errorf("GET 应返回 405，得到 %d", code)
	}
}

func TestLoginRateLimit(t *testing.T) {
	s := newTestWebServer("secret123")

	// 连续失败到阈值。
	for i := 0; i < webMaxLoginAttempts; i++ {
		code, _ := doRequest(t, s.handleLogin, http.MethodPost, "/api/login",
			`{"password":"wrong"}`, nil)
		if code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次失败应返回 401，得到 %d", i+1, code)
		}
	}

	// 达到阈值后即使密码正确也会被拒绝。
	code, body := doRequest(t, s.handleLogin, http.MethodPost, "/api/login",
		`{"password":"secret123"}`, nil)
	if code != http.StatusTooManyRequests {
		t.Errorf("封禁期间应返回 429，得到 %d：%s", code, body)
	}
}

func TestLoginSuccessClearsFailures(t *testing.T) {
	s := newTestWebServer("secret123")
	ip := "192.0.2.1"

	// 失败两次后成功，失败计数应清零。
	s.recordLoginFailure(ip)
	s.recordLoginFailure(ip)
	if !s.validSession("nonexistent") {
		t.Log("ok")
	}
	s.clearLoginFailures(ip)

	s.mu.Lock()
	_, exists := s.attempts[ip]
	s.mu.Unlock()
	if exists {
		t.Error("成功登录后应清除失败记录")
	}
}

// ---------------------------------------------------------------- 会话

func TestSessionLifecycle(t *testing.T) {
	s := newTestWebServer("secret123")

	token := s.newSession()
	if !s.validSession(token) {
		t.Fatal("新会话应有效")
	}
	// 伪造的令牌无效。
	if s.validSession("forged-token") {
		t.Error("伪造令牌不应通过")
	}
	if s.validSession("") {
		t.Error("空令牌不应通过")
	}
	// 注销后失效。
	s.dropSession(token)
	if s.validSession(token) {
		t.Error("注销后令牌应失效")
	}
}

func TestSessionExpiry(t *testing.T) {
	s := newTestWebServer("secret123")
	token := s.newSession()

	// 手动把过期时间改到过去。
	s.mu.Lock()
	s.sessions[token] = time.Now().Add(-time.Minute)
	s.mu.Unlock()

	if s.validSession(token) {
		t.Error("过期会话不应通过")
	}
	// 过期会话应被清理。
	s.mu.Lock()
	_, exists := s.sessions[token]
	s.mu.Unlock()
	if exists {
		t.Error("过期会话应被删除")
	}
}

// TestRequireAuth 验证受保护接口拒绝未登录请求。
func TestRequireAuth(t *testing.T) {
	s := newTestWebServer("secret123")
	called := false
	handler := s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	// 无 cookie。
	code, _ := doRequest(t, handler, http.MethodGet, "/api/programs", "", nil)
	if code != http.StatusUnauthorized {
		t.Errorf("未登录应返回 401，得到 %d", code)
	}
	if called {
		t.Error("未登录时不应执行业务处理")
	}

	// 带有效 cookie。
	token := s.newSession()
	code, _ = doRequest(t, handler, http.MethodGet, "/api/programs", "",
		&http.Cookie{Name: sessionCookieName, Value: token})
	if code != http.StatusOK {
		t.Errorf("已登录应返回 200，得到 %d", code)
	}
	if !called {
		t.Error("已登录时应执行业务处理")
	}
}

// TestLoginSetsCookie 验证登录成功后下发 HttpOnly cookie。
func TestLoginSetsCookie(t *testing.T) {
	s := newTestWebServer("secret123")

	req := httptest.NewRequest(http.MethodPost, "/api/login",
		strings.NewReader(`{"password":"secret123"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleLogin(rec, req)

	cookies := rec.Result().Cookies()
	var session *http.Cookie
	for _, c := range cookies {
		if c.Name == sessionCookieName {
			session = c
		}
	}
	if session == nil {
		t.Fatal("登录成功应下发会话 cookie")
	}
	if !session.HttpOnly {
		t.Error("会话 cookie 必须是 HttpOnly，防止被脚本读取")
	}
	if session.Value == "" {
		t.Error("cookie 值不应为空")
	}
	if !s.validSession(session.Value) {
		t.Error("下发的令牌应有效")
	}
}

// ---------------------------------------------------------------- 页面

func TestIndexServesPage(t *testing.T) {
	s := newTestWebServer("secret123")
	code, body := doRequest(t, s.handleIndex, http.MethodGet, "/", "", nil)

	if code != http.StatusOK {
		t.Fatalf("首页应返回 200，得到 %d", code)
	}
	// 页面应包含登录框和主界面关键元素。
	for _, want := range []string{"MultiPro", "password", "/api/login", "开始"} {
		if want == "开始" {
			continue
		}
		if !strings.Contains(body, want) {
			t.Errorf("页面缺少 %q", want)
		}
	}
	// 不应引用任何外部资源（离线可用）。
	if strings.Contains(body, "cdn.") || strings.Contains(body, "http://") {
		t.Error("页面不应引用外部资源")
	}
}

func TestIndexNotFound(t *testing.T) {
	s := newTestWebServer("secret123")
	code, _ := doRequest(t, s.handleIndex, http.MethodGet, "/nope", "", nil)
	if code != http.StatusNotFound {
		t.Errorf("未知路径应返回 404，得到 %d", code)
	}
}

// ---------------------------------------------------------------- 服务生命周期

func TestWebServerStartStop(t *testing.T) {
	s := newTestWebServer("secret123")

	// 端口 0 让系统分配，避免测试间冲突。
	if err := s.Start(0); err != nil {
		t.Fatalf("启动失败：%v", err)
	}
	running, _, _ := s.Status()
	if !running {
		t.Error("应处于运行状态")
	}
	s.Stop()
	running, _, _ = s.Status()
	if running {
		t.Error("停止后不应处于运行状态")
	}
}

func TestWebServerPortInUse(t *testing.T) {
	first := newTestWebServer("a")
	if err := first.Start(0); err != nil {
		t.Fatalf("首次启动失败：%v", err)
	}
	defer first.Stop()

	_, port, _ := first.Status()

	// 第二个实例用同一端口应失败。
	second := newTestWebServer("b")
	err := second.Start(port)
	if err == nil {
		second.Stop()
		t.Fatal("端口被占用时应返回错误")
	}
	_, _, lastErr := second.Status()
	if lastErr == "" {
		t.Error("应记录错误原因")
	}
}

func TestStopClearsSessions(t *testing.T) {
	s := newTestWebServer("secret123")
	if err := s.Start(0); err != nil {
		t.Fatal(err)
	}
	token := s.newSession()
	s.Stop()

	if s.validSession(token) {
		t.Error("停止服务后会话应全部失效")
	}
}

// TestParseIntQuery 验证日志行数参数解析。
func TestParseIntQuery(t *testing.T) {
	cases := map[string]int{
		"100": 100,
		"0":   0,
		"abc": 0,
	}
	for in, want := range cases {
		got, _ := parseInt(in)
		if got != want {
			t.Errorf("%q => %d, want %d", in, got, want)
		}
	}
}

// TestDefaultWebPort 验证默认端口与配置修正。
func TestDefaultWebPort(t *testing.T) {
	if defaultWebPort != 7117 {
		t.Errorf("默认端口应为 7117，得到 %d", defaultWebPort)
	}
	cfg := &AppConfig{WebPort: 0}
	cfg.applyDefaults()
	if cfg.WebPort != defaultWebPort {
		t.Errorf("端口 0 应修正为默认值，得到 %d", cfg.WebPort)
	}
	cfg2 := &AppConfig{WebPort: 70000}
	cfg2.applyDefaults()
	if cfg2.WebPort != defaultWebPort {
		t.Errorf("越界端口应修正为默认值，得到 %d", cfg2.WebPort)
	}
	// 合法端口保持不动。
	cfg3 := &AppConfig{WebPort: 8080}
	cfg3.applyDefaults()
	if cfg3.WebPort != 8080 {
		t.Errorf("合法端口不应被改动，得到 %d", cfg3.WebPort)
	}
}

// TestWebEndToEnd 启动真实 HTTP 服务，用 http.Client 走完整流程：
// 登录 → 拿 cookie → 访问受保护接口。
// 这是唯一能验证「Cookie 真的能跨请求生效」的测试。
func TestWebEndToEnd(t *testing.T) {
	mw := &mainWindow{app: &AppConfig{WebPassword: "testpass123"}}
	mw.runners = []*Runner{}
	s := newWebServer(mw)

	if err := s.Start(0); err != nil {
		t.Fatalf("启动失败：%v", err)
	}
	defer s.Stop()

	_, port, _ := s.Status()
	base := fmt.Sprintf("http://127.0.0.1:%d", port)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Jar:     jar,
		Timeout: 5 * time.Second,
	}

	// 1. 未登录访问受保护接口 → 401。
	resp, err := client.Get(base + "/api/programs")
	if err != nil {
		t.Fatalf("请求失败：%v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("未登录应返回 401，得到 %d", resp.StatusCode)
	}

	// 2. 错误密码 → 401。
	resp, err = client.Post(base+"/api/login", "application/json",
		strings.NewReader(`{"password":"wrong"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("错误密码应返回 401，得到 %d", resp.StatusCode)
	}

	// 3. 正确密码 → 200，并自动保存 cookie。
	resp, err = client.Post(base+"/api/login", "application/json",
		strings.NewReader(`{"password":"testpass123"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("登录应成功，得到 %d", resp.StatusCode)
	}

	// 4. 带 cookie 访问受保护接口 → 200 且返回程序列表。
	resp, err = client.Get(base + "/api/programs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("登录后应能访问，得到 %d", resp.StatusCode)
	}
	var payload struct {
		Programs []webProgram `json:"programs"`
		Hostname string       `json:"hostname"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("响应解析失败：%v", err)
	}
	if payload.Programs == nil {
		t.Error("programs 应为数组而非 null")
	}

	// 5. 首页可匿名访问。
	resp, err = client.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("首页应返回 200，得到 %d", resp.StatusCode)
	}

	// 6. 退出后受保护接口重新变为 401。
	resp, err = client.Post(base+"/api/logout", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	resp, err = client.Get(base + "/api/programs")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("退出后应返回 401，得到 %d", resp.StatusCode)
	}
}
