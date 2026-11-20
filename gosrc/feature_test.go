package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------- 脚本执行

func TestEffectiveMode(t *testing.T) {
	cases := []struct {
		path string
		mode string
		want string
	}{
		{"a.bat", ModeAuto, ModeCmd},
		{"a.cmd", ModeAuto, ModeCmd},
		{"a.ps1", ModeAuto, ModePowershell},
		{"a.py", ModeAuto, ModePython},
		{"a.js", ModeAuto, ModeNode},
		{"a.exe", ModeAuto, ModeDirect},
		{"a.BAT", ModeAuto, ModeCmd}, // 扩展名大小写不敏感
		{"a.bat", ModeDirect, ModeDirect},
		{"a.exe", ModePowershell, ModePowershell},
	}
	for _, c := range cases {
		cfg := &ProgramConfig{ExePath: c.path, ScriptMode: c.mode}
		if got := cfg.EffectiveMode(); got != c.want {
			t.Errorf("%s(%s) => %q, want %q", c.path, c.mode, got, c.want)
		}
	}
}

func TestScriptLabel(t *testing.T) {
	cases := map[string]string{
		"a.bat": "批处理",
		"a.ps1": "PowerShell",
		"a.py":  "Python",
		"a.js":  "Node.js",
		"a.exe": "可执行文件",
	}
	for path, want := range cases {
		cfg := &ProgramConfig{ExePath: path}
		if got := cfg.ScriptLabel(); got != want {
			t.Errorf("%s => %q, want %q", path, got, want)
		}
	}
}

func TestNeedsInterpreter(t *testing.T) {
	if (&ProgramConfig{ExePath: "a.exe"}).NeedsInterpreter() {
		t.Error("exe should not need interpreter")
	}
	if !(&ProgramConfig{ExePath: "a.bat"}).NeedsInterpreter() {
		t.Error("bat should need interpreter")
	}
	if (&ProgramConfig{ExePath: "a.bat", ScriptMode: ModeDirect}).NeedsInterpreter() {
		t.Error("explicit direct mode should not need interpreter")
	}
}

// TestBuildLaunchScript 验证脚本启动时会插入解释器并正确引用路径。
func TestBuildLaunchScript(t *testing.T) {
	dir := t.TempDir()
	bat := filepath.Join(dir, "hello.bat")
	if err := os.WriteFile(bat, []byte("@echo off\r\necho hi\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &ProgramConfig{ExePath: bat, Arguments: "-k abc"}
	exe, args, err := cfg.BuildLaunch(dir)
	if err != nil {
		t.Fatalf("BuildLaunch: %v", err)
	}
	if !strings.HasSuffix(strings.ToLower(exe), "cmd.exe") {
		t.Errorf("interpreter = %q, want cmd.exe", exe)
	}
	if !strings.Contains(args, "/c") {
		t.Errorf("args should contain /c: %q", args)
	}
	if !strings.Contains(args, bat) {
		t.Errorf("args should contain script path: %q", args)
	}
	if !strings.Contains(args, "-k abc") {
		t.Errorf("args should contain user args: %q", args)
	}
}

// TestBuildLaunchExe 验证 exe 直接运行时不插入解释器。
func TestBuildLaunchExe(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "app.exe")
	os.WriteFile(exe, []byte("x"), 0o644)

	cfg := &ProgramConfig{ExePath: exe, Arguments: "--flag"}
	gotExe, gotArgs, err := cfg.BuildLaunch(dir)
	if err != nil {
		t.Fatal(err)
	}
	if gotExe != exe {
		t.Errorf("exe = %q, want %q", gotExe, exe)
	}
	if gotArgs != "--flag" {
		t.Errorf("args = %q", gotArgs)
	}
}

func TestBuildLaunchMissingFile(t *testing.T) {
	cfg := &ProgramConfig{ExePath: "nope.bat"}
	if _, _, err := cfg.BuildLaunch(t.TempDir()); err == nil {
		t.Fatal("expected error for missing file")
	}
}

// TestScriptActuallyRuns 真实运行一个批处理脚本，确认输出可被捕获。
func TestScriptActuallyRuns(t *testing.T) {
	dir := t.TempDir()
	bat := filepath.Join(dir, "run.bat")
	os.WriteFile(bat, []byte("@echo off\r\necho SCRIPT-OK-MARKER\r\n"), 0o644)

	cfg := &ProgramConfig{
		ID: "s1", Name: "script", ExePath: bat, StopTimeout: 1000,
	}
	r := newRunner(cfg)
	var lines []string
	r.OnLog = func(_ *Runner, line string) { lines = append(lines, line) }

	if err := r.Start(dir, false); err != nil {
		t.Fatalf("start: %v", err)
	}
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) &&
		!strings.Contains(strings.Join(lines, "\n"), "SCRIPT-OK-MARKER") {
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "SCRIPT-OK-MARKER") {
		t.Errorf("script output not captured: %v", lines)
	}
	r.Stop()
	r.Kill()
}

// ---------------------------------------------------------------- 定时

func TestParseClock(t *testing.T) {
	cases := []struct {
		in     string
		h, m   int
		hasErr bool
	}{
		{"08:30", 8, 30, false},
		{"23:59", 23, 59, false},
		{"0:00", 0, 0, false},
		{"9:5", 9, 5, false},
		{"", 0, 0, true},
		{"24:00", 0, 0, true},
		{"12:60", 0, 0, true},
		{"abc", 0, 0, true},
		{"12", 0, 0, true},
	}
	for _, c := range cases {
		h, m, err := parseClock(c.in)
		if c.hasErr {
			if err == nil {
				t.Errorf("%q should fail", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q unexpected error: %v", c.in, err)
			continue
		}
		if h != c.h || m != c.m {
			t.Errorf("%q => %d:%d, want %d:%d", c.in, h, m, c.h, c.m)
		}
	}
}

func TestNormalizeClock(t *testing.T) {
	cases := map[string]string{
		"9:5":   "09:05",
		"08:30": "08:30",
		"23:59": "23:59",
		"":      "",
		"bad":   "bad",
	}
	for in, want := range cases {
		if got := normalizeClock(in); got != want {
			t.Errorf("%q => %q, want %q", in, got, want)
		}
	}
}

func TestValidateClock(t *testing.T) {
	if err := validateClock(""); err != nil {
		t.Errorf("empty should be valid: %v", err)
	}
	if err := validateClock("08:30"); err != nil {
		t.Errorf("valid time rejected: %v", err)
	}
	if err := validateClock("25:00"); err == nil {
		t.Error("invalid time accepted")
	}
}

func TestScheduleDue(t *testing.T) {
	now := time.Date(2026, 11, 20, 8, 30, 10, 0, time.Local)
	var zero time.Time

	if !scheduleDue(now, "08:30", zero) {
		t.Error("should fire at matching time")
	}
	if scheduleDue(now, "08:31", zero) {
		t.Error("should not fire at different minute")
	}
	// 同一分钟内已触发过，不应重复触发
	if scheduleDue(now, "08:30", now) {
		t.Error("should not fire twice in same minute")
	}
	// 次日同一时刻应再次触发
	nextDay := now.Add(24 * time.Hour)
	if !scheduleDue(nextDay, "08:30", now) {
		t.Error("should fire again the next day")
	}
	// 非法时间不应触发
	if scheduleDue(now, "bad", zero) {
		t.Error("invalid clock should not fire")
	}
}

// TestCheckSchedule 验证 Runner 的定时状态机。
func TestCheckSchedule(t *testing.T) {
	cfg := &ProgramConfig{
		ID:            "sch1",
		ScheduleStart: "08:30",
		ScheduleStop:  "23:00",
	}
	r := newRunner(cfg)

	at := func(h, m int) time.Time {
		return time.Date(2026, 11, 20, h, m, 0, 0, time.Local)
	}

	if got := r.CheckSchedule(at(8, 30)); got != ActionStart {
		t.Errorf("8:30 => %v, want ActionStart", got)
	}
	if got := r.CheckSchedule(at(8, 30)); got != ActionNone {
		t.Errorf("8:30 again => %v, want ActionNone", got)
	}
	if got := r.CheckSchedule(at(12, 0)); got != ActionNone {
		t.Errorf("12:00 => %v, want ActionNone", got)
	}
}

func TestScheduleSummary(t *testing.T) {
	cfg := &ProgramConfig{ScheduleStart: "08:30", ScheduleStop: "23:00", RestartEveryMin: 60}
	r := newRunner(cfg)
	summary := r.ScheduleSummary()
	for _, want := range []string{"启动 08:30", "停止 23:00", "每 60 分钟重启"} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary missing %q: %q", want, summary)
		}
	}
	if (&Runner{cfg: &ProgramConfig{}}).ScheduleSummary() != "" {
		t.Error("empty schedule should produce empty summary")
	}
}

// TestPeriodicRestartDue 验证周期重启计时。
func TestPeriodicRestartDue(t *testing.T) {
	cfg := &ProgramConfig{ID: "pr1", RestartEveryMin: 1}
	r := newRunner(cfg)
	now := time.Now()
	// 模拟进程在运行，且上一轮起点在 2 分钟前
	r.proc = &procHandle{pid: 1, done: make(chan struct{})}
	r.lastRestartCycle = now.Add(-2 * time.Minute)

	if got := r.CheckSchedule(now); got != ActionRestart {
		t.Errorf("overdue restart => %v, want ActionRestart", got)
	}
	// 刚重启过，不应立刻再次触发
	if got := r.CheckSchedule(now); got != ActionNone {
		t.Errorf("just restarted => %v, want ActionNone", got)
	}
}

// ---------------------------------------------------------------- 日志换行

// TestDumpLogCRLF 验证日志输出使用 CRLF，避免在 EDIT 控件里挤成一行。
func TestDumpLogCRLF(t *testing.T) {
	r := newRunner(&ProgramConfig{ID: "log1"})
	r.appendLog("first")
	r.appendLog("second")
	r.appendLog("third")

	dump := r.DumpLog()
	if strings.Contains(strings.ReplaceAll(dump, "\r\n", ""), "\n") {
		t.Fatalf("DumpLog must use CRLF, got %q", dump)
	}
	lines := strings.Split(dump, "\r\n")
	if len(lines) < 4 { // 三行 + 末尾空串
		t.Fatalf("expected 3 lines, got %d: %q", len(lines)-1, dump)
	}
	if lines[0] != "first" || lines[1] != "second" || lines[2] != "third" {
		t.Fatalf("unexpected lines: %q", dump)
	}
}

func TestToCRLF(t *testing.T) {
	cases := map[string]string{
		"":        "",
		"a":       "a",
		"a\nb":    "a\r\nb",
		"a\r\nb":  "a\r\nb", // 已是 CRLF 不重复转换
		"a\nb\nc": "a\r\nb\r\nc",
	}
	for in, want := range cases {
		if got := toCRLF(in); got != want {
			t.Errorf("%q => %q, want %q", in, got, want)
		}
	}
}

// ---------------------------------------------------------------- 图标

func TestProgramIconFallsBack(t *testing.T) {
	// 不存在的路径必须回退到系统图标而不是崩溃或返回 nil
	cfg := &ProgramConfig{ExePath: "definitely-missing-exe.exe"}
	icon := programIcon(cfg)
	if icon == nil {
		t.Fatal("programIcon must never return nil")
	}
}

func TestProgramIconCache(t *testing.T) {
	cfg := &ProgramConfig{ExePath: os.Args[0]}
	first := programIcon(cfg)
	second := programIcon(cfg)
	if first != second {
		t.Error("icon should be cached and reused")
	}
	clearIconCache()
	third := programIcon(cfg)
	if third == nil {
		t.Error("icon after cache clear must not be nil")
	}
	if third == first {
		t.Error("cache clear should produce a new icon object")
	}
}
