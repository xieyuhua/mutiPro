package main

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// 这些测试会真实启动 cmd.exe 子进程，但只使用无害的 ping 命令。

func TestRunnerStartStop(t *testing.T) {
	cfg := &ProgramConfig{
		ID: "r1", Name: "ping",
		ExePath:     "cmd.exe",
		Arguments:   "/c ping -n 30 127.0.0.1",
		StopTimeout: 1000,
	}
	r := newRunner(cfg)
	var sawLog atomic.Bool
	r.OnLog = func(_ *Runner, _ string) { sawLog.Store(true) }

	if err := r.Start(AppDir(), false); err != nil {
		t.Fatalf("start: %v", err)
	}
	time.Sleep(1200)

	if !r.IsRunning() || r.State() != StateRunning {
		t.Fatalf("expected running, state=%v", r.State())
	}
	if r.PID() <= 0 {
		t.Fatal("expected pid")
	}
	if !sawLog.Load() {
		t.Fatal("expected log output")
	}

	r.Stop()
	if r.IsRunning() || r.State() != StateStopped {
		t.Fatalf("expected stopped, state=%v", r.State())
	}

	// 停止后可以再次启动
	if err := r.Start(AppDir(), false); err != nil {
		t.Fatalf("restart: %v", err)
	}
	time.Sleep(300)
	if !r.IsRunning() {
		t.Fatal("expected running after restart")
	}
	r.Stop()
	r.Kill()
}

func TestRunnerAutoRestart(t *testing.T) {
	cfg := &ProgramConfig{
		ID: "r2", Name: "crash",
		// ping -n 60 需要约 59 秒，远超测试时长：
		// 只有「首次启动后被强制结束」才会触发重启，
		// 这样重启后的新进程在测试期间会一直存活。
		ExePath:      "cmd.exe",
		Arguments:    "/c ping -n 60 127.0.0.1",
		AutoRestart:  true,
		RestartDelay: 300,
		StopTimeout:  5000,
	}
	r := newRunner(cfg)
	var crashes atomic.Int32
	r.OnCrashed = func(_ *Runner, _ int) { crashes.Add(1) }

	if err := r.Start(AppDir(), false); err != nil {
		t.Fatalf("start: %v", err)
	}
	time.Sleep(500)

	// 模拟崩溃：直接杀掉进程，但不经过 Stop()，因此不应设置 stopWanted。
	r.mu.Lock()
	pid := 0
	if r.proc != nil {
		pid = r.proc.pid
	}
	r.mu.Unlock()
	if pid == 0 {
		t.Fatal("expected running process")
	}
	killTree(pid)

	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) && crashes.Load() == 0 {
		time.Sleep(100)
	}
	if crashes.Load() == 0 {
		t.Fatalf("expected auto restart event, state=%v", r.State())
	}

	// 重启是异步的：轮询等待新进程出现。
	restarted := false
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if r.IsRunning() && r.PID() != pid {
			restarted = true
			break
		}
		time.Sleep(100)
	}
	if !restarted {
		t.Fatalf("expected a new running process after restart, state=%v", r.State())
	}
	r.Kill()
}

func TestRunnerNoRestartAfterStop(t *testing.T) {
	cfg := &ProgramConfig{
		ID: "r3", Name: "manual",
		ExePath:      "cmd.exe",
		Arguments:    "/c ping -n 30 127.0.0.1",
		AutoRestart:  true,
		RestartDelay: 300,
		StopTimeout:  800,
	}
	r := newRunner(cfg)
	var crashes atomic.Int32
	r.OnCrashed = func(_ *Runner, _ int) { crashes.Add(1) }

	if err := r.Start(AppDir(), false); err != nil {
		t.Fatalf("start: %v", err)
	}
	time.Sleep(400)
	r.Stop()
	time.Sleep(1500)

	if crashes.Load() != 0 {
		t.Fatalf("stop must not trigger restart, got %d", crashes.Load())
	}
	if r.IsRunning() {
		t.Fatal("expected still stopped")
	}
	r.Kill()
}

func TestRunnerMissingExe(t *testing.T) {
	cfg := &ProgramConfig{ID: "r4", Name: "missing", ExePath: "definitely-not-here.exe"}
	r := newRunner(cfg)
	if err := r.Start(AppDir(), false); err == nil {
		t.Fatal("expected error for missing exe")
	}
	if r.State() != StateStopped {
		t.Fatalf("state after failure = %v", r.State())
	}
}

func TestRunnerKillIsImmediate(t *testing.T) {
	cfg := &ProgramConfig{
		ID: "r5", Name: "kill",
		ExePath:     "cmd.exe",
		Arguments:   "/c ping -n 60 127.0.0.1",
		StopTimeout: 30000, // 故意很长，验证 Kill 不等待它
	}
	r := newRunner(cfg)
	if err := r.Start(AppDir(), false); err != nil {
		t.Fatalf("start: %v", err)
	}
	time.Sleep(300)
	start := time.Now()
	r.Kill()
	// 强制结束应很快生效
	for r.IsRunning() && time.Since(start) < 5*time.Second {
		time.Sleep(50)
	}
	if r.IsRunning() {
		t.Fatal("kill did not terminate process")
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("kill took too long: %v", elapsed)
	}
}

func TestRunnerProbe(t *testing.T) {
	cfg := &ProgramConfig{
		ID: "r6", Name: "probe",
		ExePath:       "cmd.exe",
		Arguments:     "/c ping -n 30 127.0.0.1",
		ProbeEnabled:  true,
		ProbeCommand:  `/c echo Connection status: Connected && echo Name: box`,
		ProbeParser:   "KeyValue",
		ProbeOKKey:    "Connection status",
		ProbeOKValue:  "Connected",
		ProbeInterval: 1000,
		StopTimeout:   800,
	}
	r := newRunner(cfg)
	// 未运行时不应探测
	if got := r.Probe(AppDir()); got != nil {
		t.Fatal("probe should return nil when not running")
	}
	if err := r.Start(AppDir(), false); err != nil {
		t.Fatalf("start: %v", err)
	}
	time.Sleep(500)

	result := r.Probe(AppDir())
	if result == nil {
		t.Fatal("expected probe result")
	}
	if len(result.Fields) < 2 {
		t.Fatalf("fields = %d, want >= 2: %+v", len(result.Fields), result.Fields)
	}
	if result.Level != ProbeOK {
		t.Fatalf("level = %v, want ProbeOK (raw=%q)", result.Level, result.Raw)
	}
	found := false
	for _, f := range result.Fields {
		if f.Key == "Connection status" && strings.Contains(f.Value, "Connected") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected parsed field, got %+v", result.Fields)
	}
	r.Stop()
	r.Kill()
}

func TestProbeDueThrottle(t *testing.T) {
	cfg := &ProgramConfig{
		ID: "r7", Name: "due",
		ExePath:       "cmd.exe",
		Arguments:     "/c ping -n 30 127.0.0.1",
		ProbeEnabled:  true,
		ProbeCommand:  "/c echo ok",
		ProbeInterval: 2000,
		StopTimeout:   800,
	}
	r := newRunner(cfg)
	if r.ProbeDue() {
		t.Fatal("not running: ProbeDue must be false")
	}
	if err := r.Start(AppDir(), false); err != nil {
		t.Fatalf("start: %v", err)
	}
	time.Sleep(300)
	if !r.ProbeDue() {
		t.Fatal("expected due on first check")
	}
	r.MarkProbed()
	if r.ProbeDue() {
		t.Fatal("expected throttled right after probe")
	}
	r.Stop()
	r.Kill()
}
