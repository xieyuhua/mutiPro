package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestElevationLogPath 验证每次启动生成的日志路径互不冲突。
func TestElevationLogPath(t *testing.T) {
	l1, err := elevationLogPath()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(l1)
	time.Sleep(2 * time.Millisecond)
	l2, err := elevationLogPath()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(l2)

	if l1 == l2 {
		t.Fatal("log paths must be unique per launch")
	}
	if filepath.Dir(l1) != filepath.Dir(l2) {
		t.Fatal("log paths should share one directory")
	}
	if !strings.HasSuffix(l1, ".log") {
		t.Fatalf("unexpected log name: %s", l1)
	}
}

// TestDrainElevationLog 验证增量读取：只转发新增内容。
func TestDrainElevationLog(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "out.log")
	os.WriteFile(logFile, []byte("first line\r\n"), 0o644)

	r := newRunner(&ProgramConfig{ID: "e1"})
	var lines []string
	r.OnLog = func(_ *Runner, line string) { lines = append(lines, line) }
	handle := &procHandle{logFile: logFile}

	r.drainElevationLog(handle)
	if len(lines) != 1 || lines[0] != "first line" {
		t.Fatalf("first drain = %v", lines)
	}

	f, _ := os.OpenFile(logFile, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("second line\r\n")
	f.Close()

	r.drainElevationLog(handle)
	if len(lines) != 2 || lines[1] != "second line" {
		t.Fatalf("second drain = %v", lines)
	}

	// 没有新增时不应重复输出
	r.drainElevationLog(handle)
	if len(lines) != 2 {
		t.Fatalf("idle drain duplicated lines: %v", lines)
	}
}

// TestCleanupElevationFiles 确保临时文件被清理。
func TestCleanupElevationFiles(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "a.log")
	os.WriteFile(logFile, []byte("x"), 0o644)

	cleanupElevationFiles(&procHandle{logFile: logFile})
	if _, err := os.Stat(logFile); !os.IsNotExist(err) {
		t.Error("log not cleaned")
	}
	cleanupElevationFiles(nil)
	cleanupElevationFiles(&procHandle{})
}

// TestCmdWrapperCapturesOutput 真实执行提权包装使用的 cmd 命令行，
// 确认 stdout / stderr / 退出码三者都能拿到。
// 这是「提权程序看不到日志」这个问题的核心验证。
func TestCmdWrapperCapturesOutput(t *testing.T) {
	if _, err := exec.LookPath("cmd.exe"); err != nil {
		t.Skip("cmd.exe not available")
	}
	dir := t.TempDir()
	bat := filepath.Join(dir, "fail.bat")
	batContent := "@echo off\r\n" +
		"echo CONNECTING-TO-SERVER\r\n" +
		"echo FATAL: port 8088 already in use 1>&2\r\n" +
		"exit /b 3\r\n"
	if err := os.WriteFile(bat, []byte(batContent), 0o644); err != nil {
		t.Fatal(err)
	}

	logFile := filepath.Join(dir, "out.log")
	inner := `/c ""` + quoteArg(bat) + `" > "` + logFile + `" 2>&1"`

	cmd := exec.Command("cmd.exe")
	cmd.Dir = dir
	cmd.SysProcAttr = hiddenProcAttr()
	cmd.SysProcAttr.CmdLine = "cmd.exe " + inner

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			t.Fatalf("cmd run failed: %v", err)
		}
	}
	if exitCode != 3 {
		t.Errorf("exit code = %d, want 3", exitCode)
	}

	raw, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("log missing: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, "CONNECTING-TO-SERVER") {
		t.Errorf("stdout not captured: %q", text)
	}
	if !strings.Contains(text, "port 8088 already in use") {
		t.Errorf("stderr not captured: %q", text)
	}
}

// TestCmdWrapperWithArgs 验证带参数时引号不被破坏。
func TestCmdWrapperWithArgs(t *testing.T) {
	if _, err := exec.LookPath("cmd.exe"); err != nil {
		t.Skip("cmd.exe not available")
	}
	dir := t.TempDir()
	bat := filepath.Join(dir, "echoargs.bat")
	if err := os.WriteFile(bat, []byte("@echo off\r\necho ARGS=[%*]\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	logFile := filepath.Join(dir, "out.log")
	inner := `/c ""` + quoteArg(bat) + `" -k token123 -s "host:8004" > "` + logFile + `" 2>&1"`

	cmd := exec.Command("cmd.exe")
	cmd.SysProcAttr = hiddenProcAttr()
	cmd.SysProcAttr.CmdLine = "cmd.exe " + inner
	_ = cmd.Run()

	raw, _ := os.ReadFile(logFile)
	text := string(raw)
	if !strings.Contains(text, "-k token123") {
		t.Errorf("args lost: %q", text)
	}
	if !strings.Contains(text, "host:8004") {
		t.Errorf("quoted arg lost: %q", text)
	}
}
