package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSanitizeName 验证程序名能安全用作目录名。
func TestSanitizeName(t *testing.T) {
	cases := map[string]string{
		"wb2api":         "wb2api",
		"运维端口":           "运维端口",
		`a/b\c:d*e?f"g<h>i|j`: "a_b_c_d_e_f_g_h_i_j",
		"  尾部空格  ":       "尾部空格",
		"结尾点.":            "结尾点",
		"":               "未命名",
		"   ":            "未命名",
	}
	for in, want := range cases {
		if got := sanitizeName(in); got != want {
			t.Errorf("%q => %q, want %q", in, got, want)
		}
	}
}

// TestLogWriterWritesDailyFile 验证输出写入当天日期的文件。
func TestLogWriterWritesDailyFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)

	w := newLogWriter("测试程序", 30, true)
	if w == nil || w.failed {
		t.Fatal("writer should be usable")
	}
	defer w.Close()

	w.Write("第一行输出")
	w.Write("第二行输出")

	today := time.Now().Format("2006-01-02")
	path := filepath.Join(dir, "MultiPro", "logs", "测试程序", today+".log")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("log file missing: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, "第一行输出") || !strings.Contains(text, "第二行输出") {
		t.Errorf("content = %q", text)
	}
	// 每行应带时间戳。
	if !strings.Contains(text, time.Now().Format("2006-01-02")) {
		t.Errorf("missing timestamp: %q", text)
	}
	// 使用 CRLF，便于记事本正确显示。
	if !strings.Contains(text, "\r\n") {
		t.Errorf("should use CRLF: %q", text)
	}
}

// TestLogWriterDisabled 验证关闭后不创建任何文件。
func TestLogWriterDisabled(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)

	w := newLogWriter("关闭测试", 30, false)
	w.Write("不应写入")
	w.Close()

	logsRoot := filepath.Join(dir, "MultiPro", "logs")
	if entries, err := os.ReadDir(logsRoot); err == nil && len(entries) > 0 {
		t.Errorf("disabled writer should not create files, found %d entries", len(entries))
	}
}

// TestLogWriterAppends 验证多次写入是追加而不是覆盖。
func TestLogWriterAppends(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)

	first := newLogWriter("追加测试", 30, true)
	first.Write("来自第一次运行")
	first.Close()

	second := newLogWriter("追加测试", 30, true)
	second.Write("来自第二次运行")
	second.Close()

	today := time.Now().Format("2006-01-02")
	path := filepath.Join(dir, "MultiPro", "logs", "追加测试", today+".log")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, "来自第一次运行") {
		t.Error("previous run content lost")
	}
	if !strings.Contains(text, "来自第二次运行") {
		t.Error("new content missing")
	}
}

// TestLogWriterCleanupByKeepDays 验证过期文件被删除，近期文件保留。
func TestLogWriterCleanupByKeepDays(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)

	w := newLogWriter("清理测试", 7, true)
	defer w.Close()

	programDir := filepath.Join(dir, "MultiPro", "logs", "清理测试")
	if err := os.MkdirAll(programDir, 0o755); err != nil {
		t.Fatal(err)
	}

	today := time.Now()
	makeFile := func(daysAgo int) string {
		date := today.AddDate(0, 0, -daysAgo).Format("2006-01-02")
		path := filepath.Join(programDir, date+".log")
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}

	recent := makeFile(2)
	old := makeFile(30)
	// 不符合命名规则的文件不应被删除。
	other := filepath.Join(programDir, "notes.txt")
	if err := os.WriteFile(other, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}

	w.cleanup()

	if _, err := os.Stat(recent); err != nil {
		t.Error("recent log should be kept")
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("expired log should be deleted")
	}
	if _, err := os.Stat(other); err != nil {
		t.Error("unrelated file should be kept")
	}
}

// TestLogWriterKeepZeroDisablesCleanup 验证保留天数为 0 时不删除任何文件。
func TestLogWriterKeepZeroDisablesCleanup(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)

	w := newLogWriter("永久保留", 0, true)
	defer w.Close()

	programDir := filepath.Join(dir, "MultiPro", "logs", "永久保留")
	oldDate := time.Now().AddDate(-1, 0, 0).Format("2006-01-02")
	oldPath := filepath.Join(programDir, oldDate+".log")
	if err := os.WriteFile(oldPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	w.cleanup()

	if _, err := os.Stat(oldPath); err != nil {
		t.Error("keep=0 should never delete logs")
	}
}

// TestRunnerSetLogFile 验证运行中切换日志开关不崩溃，且能写入。
func TestRunnerSetLogFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)

	cfg := &ProgramConfig{
		ID: "lf1", Name: "日志测试",
		ExePath: "cmd.exe", Arguments: "/c ping -n 20 127.0.0.1",
		StopTimeout: 800,
	}
	r := newRunner(cfg)
	r.SetLogFile(true, 30)
	defer r.CloseLogFile()

	if err := r.Start(AppDir(), false); err != nil {
		t.Fatalf("start: %v", err)
	}
	time.Sleep(1200)
	r.Stop()

	today := time.Now().Format("2006-01-02")
	path := filepath.Join(dir, "MultiPro", "logs", "日志测试", today+".log")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("log file not created: %v", err)
	}
	raw, _ := os.ReadFile(path)
	if len(raw) == 0 {
		t.Error("log file should have content")
	}

	// 关闭后不再写文件，但不应报错。
	r.SetLogFile(false, 30)
	r.SetLogFile(true, 30)
	r.Kill()
}

// TestLogDirUnderConfigDir 验证日志目录位于配置目录下。
func TestLogDirUnderConfigDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)

	logPath, err := logDir()
	if err != nil {
		t.Fatal(err)
	}
	configPath, err := configDir()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(logPath) != configPath {
		t.Errorf("logDir %q should be under %q", logPath, configPath)
	}
	if !strings.HasSuffix(logPath, "logs") {
		t.Errorf("unexpected log dir: %q", logPath)
	}
}

// TestLogKeepDaysDefault 验证旧配置升级后保留天数默认为 30 天，
// 而不是 0（0 的含义是「永久保留」，与默认语义不同）。
func TestLogKeepDaysDefault(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)

	path, _ := configPath()
	// 旧配置：没有 log* 字段。
	old := `{"programs":[],"minimizeToTray":true}`
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := loadConfig()
	if !cfg.LogToFile {
		t.Error("升级后应默认启用日志文件")
	}
	if cfg.LogKeepDays != 30 {
		t.Errorf("默认保留天数应为 30，得到 %d", cfg.LogKeepDays)
	}
}

// TestLogKeepDaysUserChoice 验证用户选择「永久保留」(0) 会被保留。
func TestLogKeepDaysUserChoice(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)

	path, _ := configPath()
	user := `{"programs":[],"logToFile":true,"logKeepDays":0,"logFileInitialized":true}`
	if err := os.WriteFile(path, []byte(user), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := loadConfig()
	if cfg.LogKeepDays != 0 {
		t.Errorf("用户的永久保留选择被覆盖：%d", cfg.LogKeepDays)
	}
}
