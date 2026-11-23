package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------- 事件开关

func TestNotifyConfigWants(t *testing.T) {
	cfg := defaultNotifyConfig()
	cfg.Webhook = "https://example.com/hook"

	// 默认只开崩溃/启动失败/卡死，定时任务默认关闭。
	cfg.Enabled = true
	if !cfg.wants(EventCrash) {
		t.Error("崩溃应默认通知")
	}
	if !cfg.wants(EventStartFailed) {
		t.Error("启动失败应默认通知")
	}
	if !cfg.wants(EventHung) {
		t.Error("卡死应默认通知")
	}
	if cfg.wants(EventScheduled) {
		t.Error("定时任务默认不应通知（过于频繁）")
	}

	// 总开关关闭时任何事件都不发。
	cfg.Enabled = false
	if cfg.wants(EventCrash) {
		t.Error("总开关关闭后不应通知")
	}

	// 没有 Webhook 也不应尝试发送。
	cfg.Enabled = true
	cfg.Webhook = ""
	if cfg.wants(EventCrash) {
		t.Error("没有 Webhook 时不应通知")
	}
}

func TestEffectiveWebhook(t *testing.T) {
	cfg := defaultNotifyConfig()
	cfg.Webhook = "https://global.example.com/hook"

	// 程序没配就用全局。
	if got := cfg.effectiveWebhook(&ProgramConfig{}); got != cfg.Webhook {
		t.Errorf("应使用全局地址，得到 %q", got)
	}
	// 程序配了就用程序的。
	custom := &ProgramConfig{NotifyWebhook: "https://custom.example.com/hook"}
	if got := cfg.effectiveWebhook(custom); got != custom.NotifyWebhook {
		t.Errorf("应使用程序专属地址，得到 %q", got)
	}
	// nil 程序安全返回全局。
	if got := cfg.effectiveWebhook(nil); got != cfg.Webhook {
		t.Errorf("nil 程序应回退全局，得到 %q", got)
	}
}

// ---------------------------------------------------------------- 消息组装

func TestNotifyMessageBuildText(t *testing.T) {
	cfg := defaultNotifyConfig()
	cfg.LogTailLines = 3

	msg := NotifyMessage{
		Event:      EventCrash,
		Program:    "运维端口",
		Detail:     "退出码 1，已运行 2 小时 3 分",
		LogTail:    []string{"line1", "line2", "line3", "line4"},
		OccurredAt: time.Date(2026, 11, 20, 16, 30, 0, 0, time.Local),
		Hostname:   "DESKTOP-TEST",
	}
	text := msg.buildText(&cfg)

	for _, want := range []string{
		"【MultiPro】异常退出", "程序：运维端口", "退出码 1", "2026-11-20 16:30:00", "DESKTOP-TEST",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("消息缺少 %q：\n%s", want, text)
		}
	}
	// 只保留最后 3 行，line1 应被丢弃。
	if strings.Contains(text, "line1") {
		t.Errorf("应只保留最近 3 行：\n%s", text)
	}
	for _, want := range []string{"line2", "line3", "line4"} {
		if !strings.Contains(text, want) {
			t.Errorf("缺少尾部日志 %q：\n%s", want, text)
		}
	}
}

func TestNotifyMessageRespectsWeComLimit(t *testing.T) {
	cfg := defaultNotifyConfig()
	cfg.LogTailLines = 50

	// 每行 500 字节，50 行远超企业微信 4KB 限制。
	longLine := strings.Repeat("x", 500)
	var tail []string
	for i := 0; i < 50; i++ {
		tail = append(tail, longLine)
	}
	msg := NotifyMessage{
		Event:      EventCrash,
		Program:    "大日志",
		LogTail:    tail,
		OccurredAt: time.Now(),
	}
	text := msg.buildText(&cfg)
	if len(text) > wecomMaxBytes {
		t.Errorf("消息长度 %d 超过企业微信上限 %d", len(text), wecomMaxBytes)
	}
	if len(text) == 0 {
		t.Error("不应产生空消息")
	}
}

func TestNotifyMessageWithoutLogTail(t *testing.T) {
	cfg := defaultNotifyConfig()
	cfg.IncludeLogTail = false
	msg := NotifyMessage{
		Event:      EventCrash,
		Program:    "p",
		LogTail:    []string{"should not appear"},
		OccurredAt: time.Now(),
	}
	text := msg.buildText(&cfg)
	if strings.Contains(text, "should not appear") {
		t.Errorf("关闭日志附加后不应包含日志：%s", text)
	}
	if strings.Contains(text, "最近日志") {
		t.Error("不应出现日志标题")
	}
}

func TestNotifyEventLabels(t *testing.T) {
	cases := map[NotifyEvent]string{
		EventCrash:       "异常退出",
		EventStartFailed: "启动失败",
		EventHung:        "进程无响应",
		EventScheduled:   "定时任务",
	}
	for event, want := range cases {
		if got := event.Label(); got != want {
			t.Errorf("%s => %q, want %q", event, got, want)
		}
	}
}

// ---------------------------------------------------------------- 钉钉加签

func TestSignDingTalkURL(t *testing.T) {
	webhook := "https://oapi.dingtalk.com/robot/send?access_token=abc"
	secret := "SECtest123"
	now := time.UnixMilli(1700000000000)

	signed, err := signDingTalkURL(webhook, secret, now)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	parsed, err := url.Parse(signed)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()

	// timestamp 应为毫秒时间戳。
	if got := query.Get("timestamp"); got != "1700000000000" {
		t.Errorf("timestamp = %q", got)
	}
	// 原有 access_token 必须保留。
	if got := query.Get("access_token"); got != "abc" {
		t.Errorf("access_token = %q", got)
	}
	// sign 字段应非空且已做 URL 编码。
	sign := query.Get("sign")
	if sign == "" {
		t.Fatal("sign 不能为空")
	}
	// 同一个输入必须得到一致的签名。
	again, _ := signDingTalkURL(webhook, secret, now)
	if again != signed {
		t.Error("相同输入应产生相同签名")
	}
	// 不同密钥应产生不同签名。
	other, _ := signDingTalkURL(webhook, "different", now)
	if other == signed {
		t.Error("不同密钥应产生不同签名")
	}
}

func TestSignDingTalkURLInvalid(t *testing.T) {
	if _, err := signDingTalkURL("://bad", "s", time.Now()); err == nil {
		t.Error("非法 URL 应返回错误")
	}
}

// ---------------------------------------------------------------- 发送（本地模拟服务）

// mockRobot 记录收到的请求，并返回指定的响应体。
type mockRobot struct {
	mu       sync.Mutex
	requests []map[string]interface{}
	paths    []string
	response string
}

func (m *mockRobot) handler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var payload map[string]interface{}
	_ = json.Unmarshal(body, &payload)

	m.mu.Lock()
	m.requests = append(m.requests, payload)
	m.paths = append(m.paths, r.URL.String())
	m.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(m.response))
}

func TestSendWeCom(t *testing.T) {
	mock := &mockRobot{response: `{"errcode":0,"errmsg":"ok"}`}
	server := httptest.NewServer(http.HandlerFunc(mock.handler))
	defer server.Close()

	notifier := newNotifier()
	if err := notifier.sendWeCom(server.URL+"/send", "hello"); err != nil {
		t.Fatalf("send: %v", err)
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.requests) != 1 {
		t.Fatalf("应发送 1 次，实际 %d", len(mock.requests))
	}
	payload := mock.requests[0]
	if payload["msgtype"] != "text" {
		t.Errorf("msgtype = %v", payload["msgtype"])
	}
	text, _ := payload["text"].(map[string]interface{})
	if text["content"] != "hello" {
		t.Errorf("content = %v", text["content"])
	}
}

func TestSendWeComError(t *testing.T) {
	mock := &mockRobot{response: `{"errcode":93000,"errmsg":"invalid webhook url"}`}
	server := httptest.NewServer(http.HandlerFunc(mock.handler))
	defer server.Close()

	notifier := newNotifier()
	err := notifier.sendWeCom(server.URL, "hello")
	if err == nil {
		t.Fatal("非零 errcode 应返回错误")
	}
	if !strings.Contains(err.Error(), "93000") {
		t.Errorf("错误信息应包含错误码：%v", err)
	}
}

func TestSendDingTalkAddsSignature(t *testing.T) {
	mock := &mockRobot{response: `{"errcode":0,"errmsg":"ok"}`}
	server := httptest.NewServer(http.HandlerFunc(mock.handler))
	defer server.Close()

	notifier := newNotifier()
	err := notifier.sendDingTalk(server.URL+"/robot/send?access_token=abc",
		"SECsecret", "hello")
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.paths) != 1 {
		t.Fatalf("应发送 1 次")
	}
	// 加签后的地址应带 timestamp 与 sign。
	if !strings.Contains(mock.paths[0], "timestamp=") ||
		!strings.Contains(mock.paths[0], "sign=") {
		t.Errorf("缺少加签参数：%s", mock.paths[0])
	}
}

func TestSendDingTalkWithoutSecret(t *testing.T) {
	mock := &mockRobot{response: `{"errcode":0}`}
	server := httptest.NewServer(http.HandlerFunc(mock.handler))
	defer server.Close()

	notifier := newNotifier()
	// 未启用加签时不应添加签名参数。
	if err := notifier.sendDingTalk(server.URL, "", "hi"); err != nil {
		t.Fatalf("send: %v", err)
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if strings.Contains(mock.paths[0], "sign=") {
		t.Errorf("未配置密钥时不应加签：%s", mock.paths[0])
	}
}

func TestSendHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("server error"))
	}))
	defer server.Close()

	notifier := newNotifier()
	err := notifier.sendWeCom(server.URL, "hello")
	if err == nil {
		t.Fatal("HTTP 500 应返回错误")
	}
}

// ---------------------------------------------------------------- 日志尾部

func TestRunnerLogTail(t *testing.T) {
	r := newRunner(&ProgramConfig{ID: "lt1"})
	for i := 1; i <= 20; i++ {
		r.appendLog("line-" + itoaForTest(i))
	}

	tail := r.LogTail(5)
	if len(tail) != 5 {
		t.Fatalf("应返回 5 行，实际 %d", len(tail))
	}
	// 应为最后 5 行且保持正序。
	if tail[0] != "line-16" || tail[4] != "line-20" {
		t.Errorf("尾部内容不对：%v", tail)
	}

	if got := r.LogTail(0); got != nil {
		t.Error("请求 0 行应返回 nil")
	}
	// 请求超过实际行数时应返回全部。
	if got := r.LogTail(100); len(got) != 20 {
		t.Errorf("应返回全部 20 行，实际 %d", len(got))
	}
}

func itoaForTest(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

// ---------------------------------------------------------------- 卡死检测

func TestCheckHang(t *testing.T) {
	r := newRunner(&ProgramConfig{ID: "h1"})
	// 模拟进程在运行。
	r.proc = &procHandle{pid: 1, done: make(chan struct{})}
	r.lastOutputAt = time.Now().Add(-20 * time.Minute)

	timeout := 10 * time.Minute
	now := time.Now()

	// 首次判定应返回 true。
	if !r.CheckHang(timeout, now) {
		t.Fatal("超时后应判定卡死")
	}
	// 同一轮不应重复返回 true。
	if r.CheckHang(timeout, now) {
		t.Error("同一轮卡死不应重复触发")
	}
	// 重启后应能再次检测。
	r.MarkHangRestarted()
	r.lastOutputAt = time.Now().Add(-20 * time.Minute)
	if !r.CheckHang(timeout, time.Now()) {
		t.Error("重启后应能再次检测")
	}
}

func TestCheckHangNotTriggered(t *testing.T) {
	r := newRunner(&ProgramConfig{ID: "h2"})
	r.proc = &procHandle{pid: 1, done: make(chan struct{})}
	r.lastOutputAt = time.Now()

	// 最近有输出，不应判定卡死。
	if r.CheckHang(10*time.Minute, time.Now()) {
		t.Error("刚有输出不应判定卡死")
	}
	// 超时为 0 表示禁用。
	r.lastOutputAt = time.Now().Add(-time.Hour)
	if r.CheckHang(0, time.Now()) {
		t.Error("超时为 0 时应禁用检测")
	}
}

func TestCheckHangOnlyWhenRunning(t *testing.T) {
	r := newRunner(&ProgramConfig{ID: "h3"})
	// 没有 proc，即进程未运行。
	r.lastOutputAt = time.Now().Add(-time.Hour)
	if r.CheckHang(10*time.Minute, time.Now()) {
		t.Error("未运行的进程不应判定卡死")
	}
}

// TestNotifierEndToEnd 验证从配置到投递的完整链路：
// 事件过滤、程序级 Webhook 覆盖、消息内容都正确。
func TestNotifierEndToEnd(t *testing.T) {
	mock := &mockRobot{response: `{"errcode":0,"errmsg":"ok"}`}
	server := httptest.NewServer(http.HandlerFunc(mock.handler))
	defer server.Close()

	cfg := defaultNotifyConfig()
	cfg.Enabled = true
	cfg.Webhook = server.URL + "/global"
	cfg.LogTailLines = 2

	notifier := newNotifier()
	msg := NotifyMessage{
		Event:      EventStartFailed,
		Program:    "apitoken",
		Detail:     "启动后 0 秒即退出（退出码 1）",
		LogTail:    []string{"old line", "bind: address already in use"},
		OccurredAt: time.Now(),
		Hostname:   "HOST",
	}
	if err := notifier.Send(&cfg, &ProgramConfig{}, msg); err != nil {
		t.Fatalf("send: %v", err)
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.requests) != 1 {
		t.Fatalf("应发送 1 次")
	}
	text, _ := mock.requests[0]["text"].(map[string]interface{})
	content, _ := text["content"].(string)
	if !strings.Contains(content, "启动失败") {
		t.Errorf("应标注事件类型：%s", content)
	}
	if !strings.Contains(content, "apitoken") {
		t.Errorf("应包含程序名：%s", content)
	}
	if !strings.Contains(content, "address already in use") {
		t.Errorf("应包含日志尾部：%s", content)
	}
	// LogTailLines=2，两行都应保留。
	if !strings.Contains(content, "old line") {
		t.Errorf("应保留 2 行日志：%s", content)
	}
}

// TestNotifyCooldownKeying 验证冷却按「程序 + 事件」分别计时。
// 同一程序反复失败只发一次；不同程序、不同事件互不影响。
func TestNotifyCooldownKeying(t *testing.T) {
	cooldown := 60 * time.Second
	sent := map[string]time.Time{}

	shouldSend := func(programID string, event NotifyEvent, now time.Time) bool {
		key := programID + "|" + string(event)
		if last, ok := sent[key]; ok && now.Sub(last) < cooldown {
			return false
		}
		sent[key] = now
		return true
	}

	base := time.Now()
	// 同一程序连续崩溃：只发第一条。
	if !shouldSend("p1", EventCrash, base) {
		t.Fatal("首次崩溃应发送")
	}
	if shouldSend("p1", EventCrash, base.Add(5*time.Second)) {
		t.Error("冷却期内不应重复发送")
	}
	if shouldSend("p1", EventCrash, base.Add(30*time.Second)) {
		t.Error("仍然在冷却期内")
	}
	// 其他程序不受影响。
	if !shouldSend("p2", EventCrash, base.Add(5*time.Second)) {
		t.Error("不同程序应独立计时")
	}
	// 同一程序的不同事件独立计时。
	if !shouldSend("p1", EventStartFailed, base.Add(5*time.Second)) {
		t.Error("不同事件应独立计时")
	}
	// 冷却结束后可以再次发送。
	if !shouldSend("p1", EventCrash, base.Add(61*time.Second)) {
		t.Error("超过冷却期应再次发送")
	}
}

// ---------------------------------------------------------------- 卡死检测配置

// TestHangSettingsPriority 验证「程序配置 > 全局默认」的优先级。
func TestHangSettingsPriority(t *testing.T) {
	w := &mainWindow{app: &AppConfig{
		HangDetect:     true,
		HangTimeoutMin: 10,
	}}

	// 跟随全局：全局开启时启用，用全局阈值。
	if enabled, timeout := w.hangSettingsFor(&ProgramConfig{}); !enabled || timeout != 10*time.Minute {
		t.Errorf("跟随全局 => %v %v", enabled, timeout)
	}
	// 程序显式开启，即使全局关闭也启用。
	if enabled, _ := w.hangSettingsFor(&ProgramConfig{HangMode: "on"}); !enabled {
		t.Error("程序设为 on 时应启用")
	}
	// 程序显式关闭，即使全局开启也禁用。
	if enabled, _ := w.hangSettingsFor(&ProgramConfig{HangMode: "off"}); enabled {
		t.Error("程序设为 off 时应禁用")
	}
	// 程序覆盖阈值。
	if _, timeout := w.hangSettingsFor(&ProgramConfig{HangTimeoutMin: 3}); timeout != 3*time.Minute {
		t.Errorf("阈值覆盖失败：%v", timeout)
	}

	// 全局关闭时，跟随全局的程序应禁用。
	w.app.HangDetect = false
	if enabled, _ := w.hangSettingsFor(&ProgramConfig{}); enabled {
		t.Error("全局关闭后跟随的程序应禁用")
	}
	if enabled, _ := w.hangSettingsFor(&ProgramConfig{HangMode: "on"}); !enabled {
		t.Error("全局关闭时程序显式 on 仍应启用")
	}
}

// TestHangTimeoutFallback 验证阈值异常时回退到 10 分钟。
func TestHangTimeoutFallback(t *testing.T) {
	w := &mainWindow{app: &AppConfig{HangDetect: true, HangTimeoutMin: 0}}
	if _, timeout := w.hangSettingsFor(&ProgramConfig{}); timeout != 10*time.Minute {
		t.Errorf("全局阈值 0 时应回退 10 分钟，得到 %v", timeout)
	}
	w.app.HangTimeoutMin = -5
	if _, timeout := w.hangSettingsFor(&ProgramConfig{}); timeout != 10*time.Minute {
		t.Errorf("负数阈值应回退 10 分钟，得到 %v", timeout)
	}
}

// TestHangModeNormalize 验证非法的 HangMode 被规范成「跟随全局」。
func TestHangModeNormalize(t *testing.T) {
	cases := map[string]string{
		"on": "on", "ON": "on", " on ": "on",
		"off": "off", "OFF": "off",
		"":     "",
		"yes":  "",
		"true": "",
		"xxx":  "",
	}
	for in, want := range cases {
		p := &ProgramConfig{HangMode: in}
		normalizeProgram(p)
		if p.HangMode != want {
			t.Errorf("%q => %q, want %q", in, p.HangMode, want)
		}
	}
}
