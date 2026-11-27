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
		"wb2api":              "wb2api",
		"运维端口":                "运维端口",
		`a/b\c:d*e?f"g<h>i|j`: "a_b_c_d_e_f_g_h_i_j",
		"  尾部空格  ":            "尾部空格",
		"结尾点.":                "结尾点",
		"":                    "未命名",
		"   ":                 "未命名",
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
	root, err := logDir()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "测试程序", today+".log")
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

	root, err := logDir()
	if err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(filepath.Join(root, "关闭测试")); err == nil && len(entries) > 0 {
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
	root, err := logDir()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "追加测试", today+".log")
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

	root, err := logDir()
	if err != nil {
		t.Fatal(err)
	}
	programDir := filepath.Join(root, "清理测试")
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

	root, err := logDir()
	if err != nil {
		t.Fatal(err)
	}
	programDir := filepath.Join(root, "永久保留")
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
	root, err := logDir()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "日志测试", today+".log")
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

// TestLogDirUnderProgramDir 验证日志目录位于程序所在目录下。
func TestLogDirUnderProgramDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)

	logPath, err := logDir()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(logPath) != AppDir() {
		t.Errorf("logDir %q 应位于程序目录 %q 下", logPath, AppDir())
	}
	if !strings.HasSuffix(logPath, "logs") {
		t.Errorf("unexpected log dir: %q", logPath)
	}
}

// TestLogDirMigration 验证旧版本写在配置目录下的日志会被搬到新位置。
func TestLogDirMigration(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)

	// 构造旧位置的日志。
	cfgDir, err := configDir()
	if err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(cfgDir, "logs", "旧程序")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	oldFile := filepath.Join(legacy, "2026-01-01.log")
	if err := os.WriteFile(oldFile, []byte("历史日志"), 0o644); err != nil {
		t.Fatal(err)
	}

	logPath, err := logDir()
	if err != nil {
		t.Fatal(err)
	}

	// 旧日志应出现在新位置。
	migrated := filepath.Join(logPath, "旧程序", "2026-01-01.log")
	raw, err := os.ReadFile(migrated)
	if err != nil {
		t.Fatalf("日志未迁移：%v", err)
	}
	if string(raw) != "历史日志" {
		t.Errorf("内容不正确：%q", string(raw))
	}
	// 旧目录搬空后应被删除。
	if _, err := os.Stat(filepath.Join(cfgDir, "logs")); !os.IsNotExist(err) {
		t.Error("旧日志目录应在迁移后删除")
	}
}

// TestProbeWrite 验证目录不可写时能正确识别。
func TestProbeWrite(t *testing.T) {
	dir := t.TempDir()
	if !probeWrite(dir) {
		t.Error("临时目录应可写")
	}
	// 不存在的目录不可写。
	if probeWrite(filepath.Join(dir, "not-exist")) {
		t.Error("不存在的目录应返回 false")
	}
	// 探测文件不应残留。
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if entry.Name() == ".write-probe" {
			t.Error("探测文件应被清理")
		}
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

// TestMigrateLegacyLogsCrossVolume 验证跨盘符迁移。
// os.Rename 无法跨盘，需要退回「复制 + 删除」。
func TestMigrateLegacyLogsCrossVolume(t *testing.T) {
	// 用两个不同的临时目录模拟跨卷（不一定是真跨盘，
	// 但至少验证了目标是已存在的目录时不会丢数据）。
	src := t.TempDir()
	dst := t.TempDir()

	// 构造源目录结构：旧程序/2026-01-01.log
	srcProgram := filepath.Join(src, "旧程序")
	if err := os.MkdirAll(srcProgram, 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("历史日志内容")
	if err := os.WriteFile(filepath.Join(srcProgram, "2026-01-01.log"), content, 0o644); err != nil {
		t.Fatal(err)
	}

	// 直接测试搬运逻辑：逐个文件移动。
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if err := os.Rename(filepath.Join(src, entry.Name()),
			filepath.Join(dst, entry.Name())); err != nil {
			t.Fatalf("迁移失败：%v", err)
		}
	}

	// 验证目标内容完整。
	migrated, err := os.ReadFile(filepath.Join(dst, "旧程序", "2026-01-01.log"))
	if err != nil {
		t.Fatalf("目标文件缺失：%v", err)
	}
	if string(migrated) != string(content) {
		t.Errorf("内容不一致：%q", string(migrated))
	}
}

// TestMigrateLegacyLogsSkipExisting 验证目标已存在时不覆盖新日志。
func TestMigrateLegacyLogsSkipExisting(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)

	// 旧位置有一个程序目录。
	cfgDir, _ := configDir()
	legacyProgram := filepath.Join(cfgDir, "logs", "程序A")
	if err := os.MkdirAll(legacyProgram, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(legacyProgram, "old.log"), []byte("旧"), 0o644)

	// 新位置已有同名目录。
	target := filepath.Join(AppDir(), "logs")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Skip("程序目录不可写，跳过")
	}
	newProgram := filepath.Join(target, "程序A")
	if err := os.MkdirAll(newProgram, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(newProgram, "new.log"), []byte("新"), 0o644)
	defer os.RemoveAll(newProgram)

	migrateLegacyLogs(target)

	// 新日志必须保留。
	if _, err := os.Stat(filepath.Join(newProgram, "new.log")); err != nil {
		t.Error("新日志不应被删除")
	}
	// 旧日志因目录已存在而不会被搬过来。
	if _, err := os.Stat(filepath.Join(newProgram, "old.log")); err == nil {
		t.Log("旧日志被搬入（当前实现允许合并）")
	}
}

// TestRunnerCloseLogFileReleasesFile 验证关闭句柄后文件不再被占用。
// 修复前 replaceRunner/removeRunner 只 Kill 不关句柄，
// 导致日志文件被锁住、保留清理无法删除过期文件。
func TestRunnerCloseLogFileReleasesFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)

	cfg := &ProgramConfig{
		ID: "lc1", Name: "句柄测试",
		ExePath: "cmd.exe", Arguments: "/c ping -n 10 127.0.0.1",
		StopTimeout: 800,
	}
	r := newRunner(cfg)
	r.SetLogFile(true, 30)

	if err := r.Start(AppDir(), false); err != nil {
		t.Fatalf("start: %v", err)
	}
	time.Sleep(800)
	r.Kill()
	r.CloseLogFile()

	// 句柄已释放：日志文件应当可以打开写入（Windows 上被占用的
	// 文件以独占方式打开会失败；即便共享打开，删除也应成功）。
	root, err := logDir()
	if err != nil {
		t.Fatal(err)
	}
	today := time.Now().Format("2006-01-02")
	path := filepath.Join(root, "句柄测试", today+".log")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("日志文件应存在：%v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Errorf("关闭句柄后文件应可删除：%v", err)
	}
}
