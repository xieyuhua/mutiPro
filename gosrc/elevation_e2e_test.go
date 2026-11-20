package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// captureLogs 收集 Runner 的日志行，供断言使用。
type captureLogs struct{ lines []string }

func (c *captureLogs) attach(r *Runner) {
	r.OnLog = func(_ *Runner, line string) { c.lines = append(c.lines, line) }
}

func (c *captureLogs) joined() string { return strings.Join(c.lines, "\n") }

// TestStartElevatedCapturesOutput 真实走 UAC 提权路径，
// 验证提权启动的目标程序输出能从日志文件读回，且退出码正确。
func TestStartElevatedCapturesOutput(t *testing.T) {
	dir := t.TempDir()
	bat := filepath.Join(dir, "fail.bat")
	content := "@echo off\r\n" +
		"echo ELEVATED-STDOUT-MARKER\r\n" +
		"echo ELEVATED-STDERR-MARKER 1>&2\r\n" +
		"exit /b 5\r\n"
	if err := os.WriteFile(bat, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	r := newRunner(&ProgramConfig{ID: "e2e", Name: "elev"})
	logs := &captureLogs{}
	logs.attach(r)

	handle, err := startElevated(r, bat, "", dir)
	if err != nil {
		t.Skipf("elevation unavailable in this environment: %v", err)
	}
	if handle.logFile == "" {
		t.Skip("elevation fell back to direct mode (cmd.exe unavailable)")
	}

	// 提权进程结束后读取日志，并把 cmd 缓冲里的内容读出来。
	exitCode := r.waitExit(handle)
	close(handle.done)
	r.drainElevationLog(handle)
	cleanupElevationFiles(handle)

	joined := logs.joined()
	if !strings.Contains(joined, "ELEVATED-STDOUT-MARKER") {
		t.Errorf("stdout not captured, lines=%v", logs.lines)
	}
	if !strings.Contains(joined, "ELEVATED-STDERR-MARKER") {
		t.Errorf("stderr not captured, lines=%v", logs.lines)
	}
	if exitCode != 5 {
		t.Errorf("exit code = %d, want 5 (cmd wrapper must propagate it)", exitCode)
	}
	t.Logf("captured %d lines, exit=%d", len(logs.lines), exitCode)
}

// TestStartElevatedLongRunning 验证长驻提权程序：启动后能持续读到输出，
// 说明日志轮询对「不会马上退出的程序」同样有效。
func TestStartElevatedLongRunning(t *testing.T) {
	dir := t.TempDir()
	bat := filepath.Join(dir, "stay.bat")
	if err := os.WriteFile(bat,
		[]byte("@echo off\r\necho STAYING-ALIVE\r\nping -n 60 127.0.0.1 >nul\r\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	r := newRunner(&ProgramConfig{ID: "e2e2", Name: "elev2"})
	logs := &captureLogs{}
	logs.attach(r)

	handle, err := startElevated(r, bat, "", dir)
	if err != nil {
		t.Skipf("elevation unavailable: %v", err)
	}

	// 轮询等待输出落盘；cmd 的缓冲会有短暂延迟。
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		r.drainElevationLog(handle)
		if strings.Contains(logs.joined(), "STAYING-ALIVE") {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	if handle.pid <= 0 {
		t.Error("expected a valid pid")
	}
	if !strings.Contains(logs.joined(), "STAYING-ALIVE") {
		t.Errorf("long-running output not captured: %v", logs.lines)
	}

	// 收尾：结束进程并清理。
	killTree(handle.pid)
	r.waitExit(handle)
	close(handle.done)
	cleanupElevationFiles(handle)
}
