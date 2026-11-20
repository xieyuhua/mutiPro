package main

import (
	"strings"
	"testing"
	"time"
)

// 这一组测试保护一个回归：编辑某个程序时，绝不能影响其他程序的运行状态。
// 早期实现用「全量重建 Runner」的方式，会把所有进程一起杀掉。

// startTestRunner 启动一个长驻 cmd 进程，返回清理函数。
func startTestRunner(t *testing.T, id, name string) *Runner {
	t.Helper()
	cfg := &ProgramConfig{
		ID:          id,
		Name:        name,
		ExePath:     "cmd.exe",
		Arguments:   "/c ping -n 60 127.0.0.1",
		StopTimeout: 1000,
	}
	r := newRunner(cfg)
	if err := r.Start(AppDir(), false); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	time.Sleep(400)
	if !r.IsRunning() {
		t.Fatalf("%s should be running", name)
	}
	return r
}

// TestReplaceRunnerKeepsOthers 验证替换一个 Runner 不会影响其他 Runner。
func TestReplaceRunnerKeepsOthers(t *testing.T) {
	a := startTestRunner(t, "a", "任务A")
	b := startTestRunner(t, "b", "任务B")
	c := startTestRunner(t, "c", "任务C")
	defer a.Kill()
	defer b.Kill()
	defer c.Kill()

	pidA, pidC := a.PID(), c.PID()

	// 模拟「编辑任务B」：重建 B 的 Runner，A 和 C 完全不参与。
	bIndex := -1
	runners := []*Runner{a, b, c}
	for i, r := range runners {
		if r == b {
			bIndex = i
		}
	}
	if bIndex < 0 {
		t.Fatal("b not found")
	}

	newCfg := b.Config().clone()
	newCfg.Name = "任务B-改"
	b.Kill()
	// Kill 是异步的，等它真正退出再断言。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && b.IsRunning() {
		time.Sleep(50 * time.Millisecond)
	}
	newB := newRunner(newCfg)
	runners[bIndex] = newB
	defer newB.Kill()

	// A 和 C 必须仍在运行，且 PID 未变。
	if !a.IsRunning() {
		t.Error("任务A 不应被影响")
	}
	if !c.IsRunning() {
		t.Error("任务C 不应被影响")
	}
	if a.PID() != pidA {
		t.Errorf("任务A PID 变化：%d -> %d", pidA, a.PID())
	}
	if c.PID() != pidC {
		t.Errorf("任务C PID 变化：%d -> %d", pidC, c.PID())
	}
	// B 被替换后处于停止状态。
	if b.IsRunning() {
		t.Error("被编辑的任务B 应已停止")
	}
	if newB.IsRunning() {
		t.Error("新的任务B 在未启动前不应运行")
	}
}

// TestRunnerKillOnlyAffectsSelf 验证 Kill 只作用于自身。
func TestRunnerKillOnlyAffectsSelf(t *testing.T) {
	a := startTestRunner(t, "k1", "A")
	b := startTestRunner(t, "k2", "B")
	defer b.Kill()

	a.Kill()
	// 等待 a 真正退出
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && a.IsRunning() {
		time.Sleep(50 * time.Millisecond)
	}

	if a.IsRunning() {
		t.Error("A 应已停止")
	}
	if !b.IsRunning() {
		t.Error("B 不应被 A 的 Kill 影响")
	}
}

// TestConfigUpdateIsDetached 验证编辑出的新配置与原配置互不影响。
func TestConfigUpdateIsDetached(t *testing.T) {
	original := &ProgramConfig{
		ID:            "cfg1",
		Name:          "原名",
		ScheduleStart: "08:30",
	}
	runner := newRunner(original)

	edited := original.clone()
	edited.Name = "新名"
	edited.ScheduleStart = "0 8 * * 1-5"

	// 编辑过程中不能改动正在使用的配置对象。
	if runner.Config().Name != "原名" {
		t.Error("编辑不应直接修改运行中的配置")
	}

	runner.updateConfig(edited)
	if runner.Config().Name != "新名" {
		t.Error("updateConfig 应生效")
	}
	if original.Name != "原名" {
		t.Error("原配置对象不应被改动")
	}
	if strings.Contains(runner.Config().ScheduleStart, "原名") {
		t.Error("unexpected state")
	}
}

// TestScheduleFieldSingleSource 验证定时只有一个字段来源。
func TestScheduleFieldSingleSource(t *testing.T) {
	cfg := &ProgramConfig{
		ScheduleStart: "0 8 * * 1-5",
		ScheduleStop:  "23:00",
	}
	// 两种写法可以混用，各自独立解析。
	start, err := ParseSchedule(cfg.ScheduleStart)
	if err != nil || start == nil || start.Cron == nil {
		t.Errorf("启动应为 cron：%v %+v", err, start)
	}
	stop, err := ParseSchedule(cfg.ScheduleStop)
	if err != nil || stop == nil || stop.Clock != "23:00" {
		t.Errorf("停止应为 HH:MM：%v %+v", err, stop)
	}
}
