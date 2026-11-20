package main

import (
	"strings"
	"testing"
	"time"
)

func TestParseCronBasic(t *testing.T) {
	cases := []struct {
		expr    string
		wantErr bool
	}{
		{"* * * * *", false},
		{"0 8 * * *", false},
		{"*/5 * * * *", false},
		{"0 8 * * 1-5", false},
		{"0 3 1 * *", false},
		{"0 4 1 1,4,7,10 *", false},
		{"30 8,12,18 * * *", false},
		{"0 0 * * 0", false},
		{"0 0 * * 7", false}, // 7 = 周日
		{"", true},
		{"* * * *", true},          // 段数不对
		{"60 * * * *", true},       // 分钟越界
		{"* 24 * * *", true},       // 小时越界
		{"* * 0 * *", true},        // 日从 1 开始
		{"* * * 13 *", true},       // 月越界
		{"* * * * 8", true},        // 周越界
		{"*/0 * * * *", true},      // 步长为 0
		{"abc * * * *", true},      // 非数字
		{"5-1 * * * *", true},      // 范围倒置
		{"1,,2 * * * *", true},     // 空列表项
		{"@daily", true},           // 不支持宏
	}
	for _, c := range cases {
		_, err := ParseCron(c.expr)
		if c.wantErr && err == nil {
			t.Errorf("%q should fail", c.expr)
		}
		if !c.wantErr && err != nil {
			t.Errorf("%q unexpected error: %v", c.expr, err)
		}
	}
}

func TestCronMatches(t *testing.T) {
	// 工作日 8:00
	spec, err := ParseCron("0 8 * * 1-5")
	if err != nil {
		t.Fatal(err)
	}
	// 2026-11-20 是周五
	friday := time.Date(2026, 11, 20, 8, 0, 0, 0, time.Local)
	if !spec.Matches(friday) {
		t.Error("should match Friday 08:00")
	}
	// 周六不应匹配
	saturday := time.Date(2026, 11, 21, 8, 0, 0, 0, time.Local)
	if spec.Matches(saturday) {
		t.Error("should not match Saturday")
	}
	// 时间不对
	if spec.Matches(time.Date(2026, 11, 20, 9, 0, 0, 0, time.Local)) {
		t.Error("should not match 09:00")
	}
}

func TestCronStep(t *testing.T) {
	spec, err := ParseCron("*/15 * * * *")
	if err != nil {
		t.Fatal(err)
	}
	for _, minute := range []int{0, 15, 30, 45} {
		now := time.Date(2026, 11, 20, 10, minute, 0, 0, time.Local)
		if !spec.Matches(now) {
			t.Errorf("should match minute %d", minute)
		}
	}
	for _, minute := range []int{1, 14, 16, 59} {
		now := time.Date(2026, 11, 20, 10, minute, 0, 0, time.Local)
		if spec.Matches(now) {
			t.Errorf("should not match minute %d", minute)
		}
	}
}

// TestCronDomDowOr 验证标准 cron 的「日与周取或」规则。
func TestCronDomDowOr(t *testing.T) {
	// 每月 1 日或每周一
	spec, err := ParseCron("0 0 1 * 1")
	if err != nil {
		t.Fatal(err)
	}
	// 2026-12-01 是周二，但因「日」匹配（1 号）应触发
	dec1 := time.Date(2026, 12, 1, 0, 0, 0, 0, time.Local)
	if !spec.Matches(dec1) {
		t.Error("should match on day-of-month 1 even if not Monday")
	}
	// 2026-11-23 是周一但非 1 号，因「周」匹配应触发
	nov23 := time.Date(2026, 11, 23, 0, 0, 0, 0, time.Local)
	if !spec.Matches(nov23) {
		t.Error("should match on Monday even if not day 1")
	}
	// 普通日不应触发
	nov24 := time.Date(2026, 11, 24, 0, 0, 0, 0, time.Local)
	if spec.Matches(nov24) {
		t.Error("should not match ordinary day")
	}
}

func TestCronSundayZeroAndSeven(t *testing.T) {
	zero, _ := ParseCron("0 0 * * 0")
	seven, _ := ParseCron("0 0 * * 7")
	// 2026-11-22 是周日
	sunday := time.Date(2026, 11, 22, 0, 0, 0, 0, time.Local)
	if !zero.Matches(sunday) {
		t.Error("0 should mean Sunday")
	}
	if !seven.Matches(sunday) {
		t.Error("7 should also mean Sunday")
	}
}

func TestCronDescribe(t *testing.T) {
	cases := []struct {
		expr string
		want []string
	}{
		{"0 8 * * *", []string{"8点", "0分"}},
		{"0 8 * * 1-5", []string{"周一", "周五"}},
		{"0 3 1 * *", []string{"1日"}},
	}
	for _, c := range cases {
		spec, err := ParseCron(c.expr)
		if err != nil {
			t.Fatalf("%s: %v", c.expr, err)
		}
		text := spec.Describe()
		if text == "" {
			t.Errorf("%s: empty description", c.expr)
		}
		for _, want := range c.want {
			if !strings.Contains(text, want) {
				t.Errorf("%s => %q, missing %q", c.expr, text, want)
			}
		}
	}
}

func TestCronDueDedup(t *testing.T) {
	spec, _ := ParseCron("0 8 * * *")
	now := time.Date(2026, 11, 20, 8, 0, 30, 0, time.Local)

	if !cronDue(now, spec, time.Time{}) {
		t.Error("should fire first time")
	}
	if cronDue(now, spec, now) {
		t.Error("should not fire twice in the same minute")
	}
	// 次日同一时刻应再次触发
	nextDay := now.AddDate(0, 0, 1)
	if !cronDue(nextDay, spec, now) {
		t.Error("should fire again next day")
	}
	if cronDue(now, nil, time.Time{}) {
		t.Error("nil spec must not fire")
	}
}

// TestSingleFieldAcceptsCron 验证同一个输入框填 cron 时按 cron 触发。
func TestSingleFieldAcceptsCron(t *testing.T) {
	cfg := &ProgramConfig{ID: "cp1", ScheduleStart: "0 8 * * *"}
	r := newRunner(cfg)

	at := func(h, m int) time.Time {
		return time.Date(2026, 11, 20, h, m, 0, 0, time.Local)
	}

	if got := r.CheckSchedule(at(8, 0)); got != ActionStart {
		t.Errorf("8:00 => %v, want ActionStart from cron", got)
	}
	if got := r.CheckSchedule(at(8, 0)); got != ActionNone {
		t.Errorf("8:00 again => %v, want ActionNone (dedup)", got)
	}
	if got := r.CheckSchedule(at(8, 30)); got != ActionNone {
		t.Errorf("8:30 => %v, want ActionNone", got)
	}
}

// TestSingleFieldAcceptsClock 验证同一个输入框填 HH:MM 时按每天触发。
func TestSingleFieldAcceptsClock(t *testing.T) {
	cfg := &ProgramConfig{ID: "cp2", ScheduleStart: "09:15"}
	r := newRunner(cfg)

	at := func(h, m int) time.Time {
		return time.Date(2026, 11, 20, h, m, 0, 0, time.Local)
	}

	if got := r.CheckSchedule(at(9, 15)); got != ActionStart {
		t.Errorf("9:15 => %v, want ActionStart", got)
	}
	if got := r.CheckSchedule(at(9, 16)); got != ActionNone {
		t.Errorf("9:16 => %v, want ActionNone", got)
	}
}

func TestCronPresetsValid(t *testing.T) {
	if len(cronPresets) < 10 {
		t.Fatalf("expected a rich preset list, got %d", len(cronPresets))
	}
	// 预设可能填 HH:MM 或 cron，统一用 ParseSchedule 校验。
	for _, preset := range cronPresets {
		sched, err := ParseSchedule(preset.Expr)
		if err != nil || sched == nil {
			t.Errorf("preset %q has invalid expression %q: %v", preset.Label, preset.Expr, err)
		}
		if preset.Label == "" || preset.Note == "" {
			t.Errorf("preset %q should have label and note", preset.Expr)
		}
	}
}

// TestScheduleSummaryMixesFormats 验证列表「定时」列能同时展示两种写法。
func TestScheduleSummaryMixesFormats(t *testing.T) {
	r := newRunner(&ProgramConfig{
		ID:            "sum1",
		ScheduleStart: "0 8 * * 1-5",
		ScheduleStop:  "23:00",
	})
	summary := r.ScheduleSummary()
	if !strings.Contains(summary, "启动 0 8 * * 1-5") {
		t.Errorf("summary should show cron start: %q", summary)
	}
	if !strings.Contains(summary, "停止 23:00") {
		t.Errorf("summary should show clock stop: %q", summary)
	}
}

// TestParseScheduleAutoDetect 验证两种写法自动识别。
func TestParseScheduleAutoDetect(t *testing.T) {
	// 空值表示不启用
	if sched, err := ParseSchedule(""); err != nil || sched != nil {
		t.Errorf("empty should be disabled, got %v err=%v", sched, err)
	}

	// HH:MM
	sched, err := ParseSchedule("8:5")
	if err != nil {
		t.Fatalf("clock parse: %v", err)
	}
	if sched.Clock != "08:05" || sched.Cron != nil {
		t.Errorf("clock parse => %+v", sched)
	}
	if sched.Display() != "08:05" {
		t.Errorf("clock display = %q", sched.Display())
	}

	// cron
	sched, err = ParseSchedule("0 8 * * 1-5")
	if err != nil {
		t.Fatalf("cron parse: %v", err)
	}
	if sched.Cron == nil || sched.Clock != "" {
		t.Errorf("cron parse => %+v", sched)
	}
	if sched.Display() != "0 8 * * 1-5" {
		t.Errorf("cron display = %q", sched.Display())
	}

	// 非法输入
	for _, bad := range []string{"25:00", "0 8 * *", "abc", "* * * * * *"} {
		if _, err := ParseSchedule(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

// TestLegacyCronMigration 验证旧配置的两个字段会合并到新字段。
func TestLegacyCronMigration(t *testing.T) {
	p := &ProgramConfig{
		ScheduleStart: "08:30",
		CronStart:     "0 8 * * 1-5",
		CronStop:      "0 23 * * *",
	}
	normalizeProgram(p)
	if p.ScheduleStart != "0 8 * * 1-5" {
		t.Errorf("old cron start should override clock, got %q", p.ScheduleStart)
	}
	if p.ScheduleStop != "0 23 * * *" {
		t.Errorf("old cron stop should migrate, got %q", p.ScheduleStop)
	}
	if p.CronStart != "" || p.CronStop != "" {
		t.Error("legacy fields should be cleared after migration")
	}
}
