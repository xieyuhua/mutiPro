package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestWebRealProgramControl 用真实子进程验证 Web 端的启停操作。
// 这是唯一能验证「Web 操作真的能控制进程」的测试。
func TestWebRealProgramControl(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)

	cfg := &ProgramConfig{
		ID: "webprog1", Name: "web测试程序",
		ExePath: "cmd.exe", Arguments: "/c ping -n 60 127.0.0.1",
		StopTimeout: 1000,
	}
	runner := newRunner(cfg)

	mw := &mainWindow{app: &AppConfig{WebPassword: "pw123456"}}
	mw.runners = []*Runner{runner}

	s := newWebServer(mw)
	if err := s.Start(0); err != nil {
		t.Fatalf("启动服务失败：%v", err)
	}
	defer func() {
		runner.Kill()
		s.Stop()
	}()

	_, port, _ := s.Status()
	base := fmt.Sprintf("http://127.0.0.1:%d", port)

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 10 * time.Second}

	// 登录。
	resp, err := client.Post(base+"/api/login", "application/json",
		strings.NewReader(`{"password":"pw123456"}`))
	if err != nil {
		t.Fatalf("登录失败：%v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("登录应成功，得到 %d", resp.StatusCode)
	}

	// 查询列表：初始应为 stopped。
	programs := fetchPrograms(t, client, base)
	if len(programs) != 1 || programs[0].Running {
		t.Fatalf("初始状态应为未运行：%+v", programs)
	}

	// 启动。
	actionResp := postAction(t, client, base, "start", "webprog1")
	if actionResp != http.StatusOK {
		t.Fatalf("启动应成功，得到 %d", actionResp)
	}

	// 等待进程真正起来。
	deadline := time.Now().Add(5 * time.Second)
	var running bool
	for time.Now().Before(deadline) {
		programs = fetchPrograms(t, client, base)
		if len(programs) == 1 && programs[0].Running {
			running = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !running {
		t.Fatalf("启动后进程应处于运行状态")
	}
	if !runner.IsRunning() {
		t.Error("Runner 应报告运行中")
	}

	// 日志接口应能读到内容。
	logResp, err := client.Get(base + "/api/log?id=webprog1&tail=50")
	if err != nil {
		t.Fatal(err)
	}
	var logPayload struct {
		Log string `json:"log"`
	}
	if err := jsonDecode(logResp.Body, &logPayload); err != nil {
		t.Fatalf("日志解析失败：%v", err)
	}
	logResp.Body.Close()
	if logResp.StatusCode != http.StatusOK {
		t.Errorf("日志接口应返回 200，得到 %d", logResp.StatusCode)
	}
	// JSON 里中文会被转义，必须解析后再比对。
	if !strings.Contains(logPayload.Log, "已启动") {
		t.Errorf("日志应包含启动记录：%q", logPayload.Log)
	}

	// 停止。
	if code := postAction(t, client, base, "stop", "webprog1"); code != http.StatusOK {
		t.Errorf("停止应成功，得到 %d", code)
	}
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if !runner.IsRunning() {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if runner.IsRunning() {
		t.Error("停止后进程不应再运行")
	}
}

// TestWebUnknownAction 验证未知操作被拒绝。
func TestWebUnknownAction(t *testing.T) {
	mw := &mainWindow{app: &AppConfig{WebPassword: "pw"}}
	mw.runners = []*Runner{}
	s := newWebServer(mw)

	token := s.newSession()
	req := newAuthorizedRequest("POST", "/api/action",
		`{"action":"evil","id":"x"}`, token)
	rec := httptest.NewRecorder()
	s.requireAuth(s.handleAction)(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("未知操作应返回 400，得到 %d", rec.Code)
	}
}

// TestWebActionMissingID 验证缺 id 时不误操作。
func TestWebActionMissingID(t *testing.T) {
	mw := &mainWindow{app: &AppConfig{WebPassword: "pw"}}
	mw.runners = []*Runner{}
	s := newWebServer(mw)

	token := s.newSession()
	req := newAuthorizedRequest("POST", "/api/action",
		`{"action":"start","id":"not-exist"}`, token)
	rec := httptest.NewRecorder()
	s.requireAuth(s.handleAction)(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("不存在的程序应返回 400，得到 %d", rec.Code)
	}
}

// ---------------------------------------------------------------- 辅助

func fetchPrograms(t *testing.T, client *http.Client, base string) []webProgram {
	t.Helper()
	resp, err := client.Get(base + "/api/programs")
	if err != nil {
		t.Fatalf("查询失败：%v", err)
	}
	defer resp.Body.Close()
	var payload struct {
		Programs []webProgram `json:"programs"`
	}
	if err := jsonDecode(resp.Body, &payload); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	return payload.Programs
}

func postAction(t *testing.T, client *http.Client, base, action, id string) int {
	t.Helper()
	body := fmt.Sprintf(`{"action":%q,"id":%q}`, action, id)
	resp, err := client.Post(base+"/api/action", "application/json",
		strings.NewReader(body))
	if err != nil {
		t.Fatalf("操作请求失败：%v", err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func newAuthorizedRequest(method, target, body, token string) *http.Request {
	req, _ := http.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	return req
}

func jsonDecode(reader io.Reader, target interface{}) error {
	return json.NewDecoder(reader).Decode(target)
}
