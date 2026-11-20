package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// CronSpec 是一个 5 段 cron 表达式的解析结果，仅支持标准字段：
//
//	分 时 日 月 周
//
// 支持 *、数字、范围 a-b、列表 a,b,c 与步长 */n、a-b/n。
// 不支持秒字段和 @daily 之类的宏，保持行为可预测。
type CronSpec struct {
	Minute, Hour, DayOfMonth, Month, DayOfWeek map[int]bool
	// DomRestricted / DowRestricted 用于实现标准 cron 的「日与周取或」规则：
	// 两者都被限定时，满足任一即可触发；只限定一个时按该字段判断。
	DomRestricted bool
	DowRestricted bool
	Raw           string
}

type cronField struct {
	name     string
	min, max int
}

var cronFields = []cronField{
	{"分钟", 0, 59},
	{"小时", 0, 23},
	{"日", 1, 31},
	{"月", 1, 12},
	{"星期", 0, 7},
}

// ParseCron 解析 5 段 cron 表达式。
func ParseCron(expr string) (*CronSpec, error) {
	text := strings.TrimSpace(expr)
	if text == "" {
		return nil, fmt.Errorf("表达式为空")
	}
	parts := strings.Fields(text)
	if len(parts) != 5 {
		return nil, fmt.Errorf("需要 5 段（分 时 日 月 周），当前 %d 段", len(parts))
	}

	sets := make([]map[int]bool, 5)
	for i, part := range parts {
		set, err := parseCronField(part, cronFields[i])
		if err != nil {
			return nil, fmt.Errorf("%s字段「%s」：%v", cronFields[i].name, part, err)
		}
		sets[i] = set
	}

	spec := &CronSpec{
		Minute:       sets[0],
		Hour:         sets[1],
		DayOfMonth:   sets[2],
		Month:        sets[3],
		DayOfWeek:    sets[4],
		DomRestricted: parts[2] != "*",
		DowRestricted: parts[4] != "*",
		Raw:           text,
	}
	// 星期日既可写 0 也可写 7，统一映射到 0。
	if spec.DayOfWeek[7] {
		spec.DayOfWeek[0] = true
	}
	return spec, nil
}

func parseCronField(part string, field cronField) (map[int]bool, error) {
	set := map[int]bool{}
	for _, item := range strings.Split(part, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			return nil, fmt.Errorf("存在空的列表项")
		}
		if err := parseCronItem(item, field, set); err != nil {
			return nil, err
		}
	}
	if len(set) == 0 {
		return nil, fmt.Errorf("没有匹配到任何值")
	}
	return set, nil
}

func parseCronItem(item string, field cronField, set map[int]bool) error {
	// 拆出步长
	step := 1
	hasStep := false
	if idx := strings.Index(item, "/"); idx >= 0 {
		stepText := strings.TrimSpace(item[idx+1:])
		item = strings.TrimSpace(item[:idx])
		value, err := strconv.Atoi(stepText)
		if err != nil || value <= 0 {
			return fmt.Errorf("步长 %q 无效", stepText)
		}
		step = value
		hasStep = true
	}

	start, end := field.min, field.max
	switch {
	case item == "*":
		// 保持默认全范围
	case strings.Contains(item, "-"):
		bounds := strings.SplitN(item, "-", 2)
		s, err := parseCronNumber(bounds[0], field)
		if err != nil {
			return err
		}
		e, err := parseCronNumber(bounds[1], field)
		if err != nil {
			return err
		}
		if s > e {
			return fmt.Errorf("范围起点 %d 大于终点 %d", s, e)
		}
		start, end = s, e
	default:
		value, err := parseCronNumber(item, field)
		if err != nil {
			return err
		}
		start, end = value, value
		// 单值带步长没有意义，按单值处理。
		hasStep = false
	}
	_ = hasStep

	for v := start; v <= end; v += step {
		set[v] = true
	}
	return nil
}

func parseCronNumber(text string, field cronField) (int, error) {
	value, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil {
		return 0, fmt.Errorf("值 %q 不是数字", text)
	}
	if value < field.min || value > field.max {
		return 0, fmt.Errorf("值 %d 超出范围 %d-%d", value, field.min, field.max)
	}
	return value, nil
}

// Matches 判断给定时刻是否满足表达式（精确到分钟）。
func (c *CronSpec) Matches(t time.Time) bool {
	if c == nil {
		return false
	}
	if !c.Minute[t.Minute()] || !c.Hour[t.Hour()] || !c.Month[int(t.Month())] {
		return false
	}

	domMatch := c.DayOfMonth[t.Day()]
	dowMatch := c.DayOfWeek[int(t.Weekday())]
	switch {
	case c.DomRestricted && c.DowRestricted:
		// 标准 cron：日与周同时限定时取「或」。
		return domMatch || dowMatch
	case c.DomRestricted:
		return domMatch
	case c.DowRestricted:
		return dowMatch
	default:
		return true
	}
}

// Describe 生成中文说明，方便界面里确认表达式含义。
func (c *CronSpec) Describe() string {
	if c == nil {
		return ""
	}
	parts := make([]string, 0, 5)
	parts = append(parts, describeSet(c.Minute, 0, 59, "分", "每分钟"))
	parts = append(parts, describeSet(c.Hour, 0, 23, "点", "每小时"))
	if c.DomRestricted {
		parts = append(parts, describeSet(c.DayOfMonth, 1, 31, "日", "每天"))
	}
	if c.DowRestricted {
		parts = append(parts, describeWeek(c.DayOfWeek))
	}
	return strings.Join(parts, " ")
}

// describeSet 把集合压缩成可读文本。
// 覆盖整个值域时返回 fallback（如「每分钟」），否则列出具体值。
func describeSet(set map[int]bool, fieldMin, fieldMax int, unit, fallback string) string {
	values := sortedKeys(set)
	if len(values) == fieldMax-fieldMin+1 {
		return fallback
	}
	if len(values) <= 6 {
		texts := make([]string, len(values))
		for i, v := range values {
			texts[i] = strconv.Itoa(v)
		}
		return strings.Join(texts, ",") + unit
	}
	return fmt.Sprintf("%d 个%s", len(values), unit)
}

func sortedKeys(set map[int]bool) []int {
	keys := make([]int, 0, len(set))
	for v := range set {
		keys = append(keys, v)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

var weekNames = map[int]string{
	0: "周日", 1: "周一", 2: "周二", 3: "周三", 4: "周四", 5: "周五", 6: "周六",
}

func describeWeek(set map[int]bool) string {
	values := sortedKeys(set)
	if len(values) == 7 {
		return "每天"
	}
	texts := make([]string, 0, len(values))
	for _, v := range values {
		if name, ok := weekNames[v]; ok {
			texts = append(texts, name)
		}
	}
	return strings.Join(texts, "、")
}

// ---------------------------------------------------------------- 预设示例

// CronPreset 是界面里可一键选择的常用表达式。
type CronPreset struct {
	Label string
	Expr  string
	Note  string
}

// cronPresets 提供常见场景，用户选中后自动填入。
// 每天固定时刻的项直接用 HH:MM 写法，比 cron 更好读；
// 其余需要按星期/按月重复的项使用 cron。
var cronPresets = []CronPreset{
	{"每天 00:00", "00:00", "每天零点"},
	{"每天 02:00", "02:00", "凌晨维护窗口"},
	{"每天 08:00", "08:00", "上班前启动"},
	{"每天 08:30", "08:30", "上班前启动"},
	{"每天 12:00", "12:00", "午间重启"},
	{"每天 18:00", "18:00", "下班后停止"},
	{"每天 23:00", "23:00", "夜间停止"},
	{"每 5 分钟", "*/5 * * * *", "轻量轮询"},
	{"每 15 分钟", "*/15 * * * *", "常规轮询"},
	{"每 30 分钟", "*/30 * * * *", "常规轮询"},
	{"每小时整点", "0 * * * *", "整点执行"},
	{"每 2 小时", "0 */2 * * *", "低频任务"},
	{"每 6 小时", "0 */6 * * *", "低频任务"},
	{"工作日 09:00", "0 9 * * 1-5", "周一至周五"},
	{"工作日 18:00", "0 18 * * 1-5", "周一至周五"},
	{"工作日每小时", "0 * * * 1-5", "周一至周五整点"},
	{"每周一 08:00", "0 8 * * 1", "每周一次"},
	{"每周日 23:00", "0 23 * * 0", "每周收尾"},
	{"每月 1 日 03:00", "0 3 1 * *", "月度维护"},
	{"每季度首日", "0 4 1 1,4,7,10 *", "季度任务"},
}
