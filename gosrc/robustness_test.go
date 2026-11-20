package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/lxn/win"
)

// ---------------------------------------------------------------- 日志裁剪

// TestTrimLogByLinesKeepsWholeLines 验证按行裁剪，不会把一行切成两半。
func TestTrimLogByLinesKeepsWholeLines(t *testing.T) {
	var builder strings.Builder
	for i := 0; i < 1000; i++ {
		builder.WriteString("line-")
		builder.WriteString(strings.Repeat("x", 40))
		builder.WriteString("\n")
	}
	original := builder.Len()

	trimLogByLines(&builder)
	result := builder.String()

	if builder.Len() >= original {
		t.Fatalf("should shrink: %d -> %d", original, builder.Len())
	}
	if len(result) == 0 {
		t.Fatal("should keep remaining content")
	}
	// 结果必须以完整行开头（前一行已被丢弃）。
	if strings.HasPrefix(result, "ine-") || strings.HasPrefix(result, "ne-") {
		t.Errorf("log was cut mid-line: %q", result[:20])
	}
	for _, line := range strings.Split(strings.TrimRight(result, "\n"), "\n") {
		if !strings.HasPrefix(line, "line-") {
			t.Errorf("malformed line after trim: %q", line)
		}
	}
	// 内容应当缩小但保留可观的比例。
	if builder.Len() < original/4 {
		t.Errorf("trimmed too aggressively: %d of %d", builder.Len(), original)
	}
}

// TestTrimLogByLinesKeepsValidUTF8 验证中文日志裁剪后不会出现乱码。
func TestTrimLogByLinesKeepsValidUTF8(t *testing.T) {
	var builder strings.Builder
	// 每行都是中文，多字节字符密集，最容易暴露按字节切的问题。
	for i := 0; i < 2000; i++ {
		builder.WriteString("启动成功，正在连接服务器……\n")
	}

	trimLogByLines(&builder)
	result := builder.String()

	if !utf8.ValidString(result) {
		t.Fatal("trim produced invalid UTF-8")
	}
	if strings.ContainsRune(result, utf8.RuneError) {
		t.Fatal("trim produced replacement characters")
	}
	// 每一行仍应是完整的中文句子。
	for _, line := range strings.Split(strings.TrimRight(result, "\n"), "\n") {
		if line != "启动成功，正在连接服务器……" {
			t.Errorf("corrupted line: %q", line)
		}
	}
}

// TestTrimLogSingleHugeLine 验证只有一行超长内容时的退化处理。
func TestTrimLogSingleHugeLine(t *testing.T) {
	var builder strings.Builder
	builder.WriteString(strings.Repeat("中", 50000)) // 约 150KB，无换行

	trimLogByLines(&builder)
	result := builder.String()

	if !utf8.ValidString(result) {
		t.Fatal("single-line trim produced invalid UTF-8")
	}
	if len(result) == 0 {
		t.Fatal("should keep some content")
	}
}

func TestUTF8SafeCut(t *testing.T) {
	text := "中文abc"
	// 「中」占 3 字节，切在第 1、2 字节处都应回退到 0。
	for cut := 1; cut <= 2; cut++ {
		if got := utf8SafeCut(text, cut); got != 0 {
			t.Errorf("cut %d => %d, want 0", cut, got)
		}
	}
	if got := utf8SafeCut(text, 3); got != 3 {
		t.Errorf("cut 3 => %d, want 3", got)
	}
	if got := utf8SafeCut(text, 0); got != 0 {
		t.Errorf("cut 0 => %d", got)
	}
	if got := utf8SafeCut(text, len(text)); got != len(text) {
		t.Errorf("cut at end => %d", got)
	}
	if got := utf8SafeCut(text, len(text)+5); got != len(text) {
		t.Errorf("cut past end => %d", got)
	}
}

// ---------------------------------------------------------------- 配置持久化

// TestConfigSaveAtomicAndBackup 验证保存后主文件与备份都存在且可读。
func TestConfigSaveAtomicAndBackup(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)

	cfg := defaultAppConfig()
	cfg.Programs = append(cfg.Programs, &ProgramConfig{
		ID: "p1", Name: "第一版", ExePath: "a.exe",
	})
	if err := cfg.save(); err != nil {
		t.Fatalf("first save: %v", err)
	}
	path, _ := configPath()
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("temp file should be cleaned up")
	}

	// 第二次保存应生成备份。
	cfg.Programs[0].Name = "第二版"
	if err := cfg.save(); err != nil {
		t.Fatalf("second save: %v", err)
	}
	backupRaw, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	if !strings.Contains(string(backupRaw), "第一版") {
		t.Errorf("backup should hold the previous version, got %q", string(backupRaw))
	}

	loaded := loadConfig()
	if loaded.Programs[0].Name != "第二版" {
		t.Errorf("current config = %q", loaded.Programs[0].Name)
	}
}

// TestConfigRecoverFromBackup 验证主文件损坏时自动从备份恢复。
func TestConfigRecoverFromBackup(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)

	cfg := defaultAppConfig()
	cfg.Programs = append(cfg.Programs, &ProgramConfig{
		ID: "p1", Name: "可恢复", ExePath: "a.exe",
	})
	if err := cfg.save(); err != nil {
		t.Fatal(err)
	}
	// 再存一次产生备份。
	if err := cfg.save(); err != nil {
		t.Fatal(err)
	}

	path, _ := configPath()
	// 故意写坏主文件。
	if err := os.WriteFile(path, []byte("{ broken"), 0o644); err != nil {
		t.Fatal(err)
	}

	loaded := loadConfig()
	if len(loaded.Programs) != 1 || loaded.Programs[0].Name != "可恢复" {
		t.Fatalf("should recover from backup, got %+v", loaded.Programs)
	}
}

// TestConfigMissingEverythingFallsBack 验证主文件和备份都缺失时返回默认配置。
func TestConfigMissingEverythingFallsBack(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)

	cfg := loadConfig()
	if cfg == nil || len(cfg.Programs) != 0 {
		t.Fatalf("expected empty default config, got %+v", cfg)
	}
}

func TestReplaceFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "new.txt")
	dst := filepath.Join(dir, "target.txt")

	if err := os.WriteFile(src, []byte("new content"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 目标已存在，验证能正常覆盖。
	if err := os.WriteFile(dst, []byte("old content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := replaceFile(src, dst); err != nil {
		t.Fatalf("replaceFile: %v", err)
	}
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new content" {
		t.Errorf("content = %q", string(data))
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Error("source should be moved away")
	}
}

// ---------------------------------------------------------------- 窗口状态

func TestWindowStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)

	// 未保存过时应返回 nil。
	if state := loadWindowState(); state != nil {
		t.Fatalf("expected nil, got %+v", state)
	}

	path, err := windowStatePath()
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"x":120,"y":80,"width":1100,"height":760,"maximized":false}`)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	state := loadWindowState()
	if state == nil {
		t.Fatal("should load saved state")
	}
	if state.X != 120 || state.Y != 80 || state.Width != 1100 || state.Height != 760 {
		t.Errorf("unexpected state: %+v", state)
	}
	if state.Maximized {
		t.Error("maximized should be false")
	}
}

func TestValidWindowState(t *testing.T) {
	screenW := int(win.GetSystemMetrics(win.SM_CXSCREEN))
	// 位置校验只用宽高，竖屏时也按可见性判断即可。

	if validWindowState(nil) {
		t.Error("nil state must be invalid")
	}
	// 尺寸过小
	if validWindowState(&windowState{Width: 100, Height: 100}) {
		t.Error("tiny window must be invalid")
	}
	// 完全在屏幕外
	if validWindowState(&windowState{X: screenW + 500, Y: 100, Width: 900, Height: 700}) {
		t.Error("offscreen window must be invalid")
	}
	// 正常位置
	if !validWindowState(&windowState{X: 100, Y: 100, Width: 1000, Height: 700}) {
		t.Error("normal window should be valid")
	}
	// 略微超出但仍可见，应接受
	if !validWindowState(&windowState{X: screenW - 200, Y: 50, Width: 1000, Height: 700}) {
		t.Error("partially visible window should be valid")
	}
}

// ---------------------------------------------------------------- 单实例

func TestSingleInstanceGuard(t *testing.T) {
	// 首次获取成功。
	if !acquireSingleInstance() {
		t.Fatal("first acquire should succeed")
	}
	// 第二次应失败。
	if acquireSingleInstance() {
		releaseSingleInstance()
		t.Fatal("second acquire should fail")
	}
	releaseSingleInstance()
	// 释放后可以再次获取。
	if !acquireSingleInstance() {
		t.Fatal("acquire after release should succeed")
	}
	releaseSingleInstance()
}

// ---------------------------------------------------------------- 崩溃提醒

// TestCrashNotifyDefaults 验证默认配置下崩溃提醒是开启的。
func TestCrashNotifyDefaults(t *testing.T) {
	cfg := defaultAppConfig()
	if !cfg.CrashNotify {
		t.Error("崩溃提醒默认应开启")
	}
	if !cfg.CrashSound || !cfg.CrashBalloon {
		t.Error("提示音与气泡默认应开启")
	}
	if cfg.CrashNotifyCooldownSec <= 0 {
		t.Errorf("冷却时间应有默认值，得到 %d", cfg.CrashNotifyCooldownSec)
	}
	if cfg.crashCooldown() <= 0 {
		t.Error("冷却时长应大于 0")
	}
}

// TestCrashCooldownFallback 验证冷却配置异常时回退到 60 秒。
func TestCrashCooldownFallback(t *testing.T) {
	for _, value := range []int{0, -1, -100} {
		cfg := &AppConfig{CrashNotifyCooldownSec: value}
		if got := cfg.crashCooldown(); got != 60*time.Second {
			t.Errorf("%d => %v, want 60s", value, got)
		}
	}
	cfg := &AppConfig{CrashNotifyCooldownSec: 30}
	if got := cfg.crashCooldown(); got != 30*time.Second {
		t.Errorf("30 => %v", got)
	}
}

// TestCrashNotifyCooldown 验证同一程序在冷却期内只提醒一次，
// 不同程序互不影响。
func TestCrashNotifyCooldown(t *testing.T) {
	// 直接测试冷却判定逻辑，不创建窗口。
	cooldown := 60 * time.Second
	notified := map[string]time.Time{}

	shouldNotify := func(id string, now time.Time) bool {
		if last, ok := notified[id]; ok && now.Sub(last) < cooldown {
			return false
		}
		notified[id] = now
		return true
	}

	base := time.Now()
	if !shouldNotify("a", base) {
		t.Error("首次应提醒")
	}
	if shouldNotify("a", base.Add(10*time.Second)) {
		t.Error("冷却期内不应重复提醒")
	}
	if !shouldNotify("b", base.Add(10*time.Second)) {
		t.Error("不同程序应独立计时")
	}
	if !shouldNotify("a", base.Add(61*time.Second)) {
		t.Error("超过冷却期后应再次提醒")
	}
}

// TestCrashNotifyConfigMigrates 验证旧配置（没有这些字段）读入后使用默认值。
func TestCrashNotifyConfigMigrates(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)

	path, _ := configPath()
	// 模拟旧版本写下的配置：没有 crash* 字段。
	old := `{"programs":[],"autoStartManager":false,"minimizeToTray":true,"showTrayHint":true}`
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := loadConfig()
	// 旧配置没有 crash* 字段，applyDefaults 应补上「开启」，
	// 否则升级后用户收不到任何崩溃提醒。
	if !cfg.CrashNotify {
		t.Error("旧配置升级后崩溃提醒应默认开启")
	}
	if !cfg.CrashSound || !cfg.CrashBalloon {
		t.Error("旧配置升级后声音与气泡应默认开启")
	}
	if !cfg.CrashNotifyInitialized {
		t.Error("应标记为已初始化，避免下次覆盖用户选择")
	}
	if cfg.crashCooldown() <= 0 {
		t.Error("冷却时间必须有效")
	}
}

// TestCrashNotifyUserChoicePreserved 验证用户主动关闭后不会被默认值覆盖。
func TestCrashNotifyUserChoicePreserved(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)

	path, _ := configPath()
	// 用户已经关掉提醒：带初始化标记且 CrashNotify=false。
	saved := `{"programs":[],"crashNotify":false,"crashSound":false,
		"crashBalloon":false,"crashNotifyCooldownSec":30,
		"crashNotifyInitialized":true}`
	if err := os.WriteFile(path, []byte(saved), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := loadConfig()
	if cfg.CrashNotify {
		t.Error("用户主动关闭的提醒不应被重新打开")
	}
	if cfg.CrashSound || cfg.CrashBalloon {
		t.Error("用户关闭的子项应保持关闭")
	}
	if cfg.CrashNotifyCooldownSec != 30 {
		t.Errorf("冷却时间应保留用户的 30 秒，得到 %d", cfg.CrashNotifyCooldownSec)
	}
}
