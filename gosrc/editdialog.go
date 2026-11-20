package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/lxn/walk"
	dcl "github.com/lxn/walk/declarative"
)

// editDialog 添加/编辑一个程序的配置。界面字段直接映射到 ProgramConfig，
// 换别的程序只需改这里填的内容，不需要改代码。
type editDialog struct {
	cfg *ProgramConfig
	dlg *walk.Dialog

	txtName     *walk.LineEdit
	txtExe      *walk.LineEdit
	txtWorkDir  *walk.LineEdit
	txtArgs     *walk.TextEdit
	txtArgsList *walk.TextEdit
	txtStop     *walk.LineEdit
	numStop     *walk.NumberEdit

	chkAdmin       *walk.CheckBox
	chkAutoRestart *walk.CheckBox
	numRestart     *walk.NumberEdit
	chkAutoStart   *walk.CheckBox

	chkProbe    *walk.CheckBox
	txtProbeCmd *walk.LineEdit
	cmbParser   *walk.ComboBox
	txtOKKey    *walk.LineEdit
	txtOKValue  *walk.LineEdit
	numInterval *walk.NumberEdit

	cmbMode     *walk.ComboBox
	txtInterp   *walk.LineEdit
	lblModeHint *walk.Label

	// 启动/停止各只有一个输入框，内容可以是 HH:MM 或 cron。
	txtSchedStart *walk.LineEdit
	txtSchedStop  *walk.LineEdit
	lblCronHint   *walk.Label
	numRestartMin *walk.NumberEdit

	accepted bool
}

var parserOptions = []string{"Lines（原样行）", "KeyValue（键: 值）", "Json"}

// modeOptions 与 ProgramConfig.ScriptMode 一一对应。
var modeOptions = []string{
	"自动识别（按扩展名）",
	"直接运行",
	"以 cmd 运行（.bat/.cmd）",
	"以 PowerShell 运行（.ps1）",
	"以 Python 运行（.py）",
	"以 Node.js 运行（.js）",
}

func modeIndex(mode string) int {
	switch strings.ToLower(mode) {
	case ModeDirect:
		return 1
	case ModeCmd:
		return 2
	case ModePowershell:
		return 3
	case ModePython:
		return 4
	case ModeNode:
		return 5
	default:
		return 0
	}
}

func modeValue(index int) string {
	switch index {
	case 1:
		return ModeDirect
	case 2:
		return ModeCmd
	case 3:
		return ModePowershell
	case 4:
		return ModePython
	case 5:
		return ModeNode
	default:
		return ModeAuto
	}
}

// showProgramDialog 打开编辑对话框。返回编辑后的副本和是否确认。
func showProgramDialog(owner walk.Form, source *ProgramConfig) (*ProgramConfig, bool) {
	d := &editDialog{cfg: source.clone()}
	if err := d.build(owner); err != nil {
		walk.MsgBox(owner, "打开编辑窗口失败", err.Error(), walk.MsgBoxIconError)
		return nil, false
	}
	if d.dlg == nil {
		return nil, false
	}
	d.initAfterCreate()
	d.dlg.Run()
	if !d.accepted {
		return nil, false
	}
	return d.cfg, true
}

func (d *editDialog) build(owner walk.Form) error {
	err := dcl.Dialog{
		AssignTo: &d.dlg,
		Title:    "编辑程序",
		Icon:     appIcon(),
		MinSize:  dcl.Size{Width: 760, Height: 620},
		Size:     dcl.Size{Width: 820, Height: 820},
		Layout:   dcl.VBox{Spacing: 8},
		Children: []dcl.Widget{
			dcl.ScrollView{
				Layout: dcl.VBox{Spacing: 6, Alignment: dcl.AlignHNearVNear},
				Children: []dcl.Widget{
					// ---- 基本配置 ----
					dcl.GroupBox{
						Title:  "基本",
						Layout: dcl.Grid{Columns: 2, Spacing: 6},
						Children: []dcl.Widget{
							dcl.Label{Text: "名称"},
							dcl.LineEdit{AssignTo: &d.txtName, Text: d.cfg.Name},

							dcl.Label{Text: "程序路径 *"},
							dcl.Composite{
								Layout: dcl.HBox{Spacing: 4, MarginsZero: true},
								Children: []dcl.Widget{
									dcl.LineEdit{AssignTo: &d.txtExe, Text: d.cfg.ExePath, StretchFactor: 1},
									dcl.PushButton{Text: "浏览…", OnClicked: func() { d.browseExe() }},
								},
							},

							dcl.Label{Text: "工作目录"},
							dcl.LineEdit{
								AssignTo:    &d.txtWorkDir,
								Text:        d.cfg.WorkDir,
								ToolTipText: "留空则使用程序所在目录，支持相对路径",
							},

							dcl.Label{Text: "命令行参数"},
							dcl.TextEdit{
								AssignTo:    &d.txtArgs,
								Text:        d.cfg.Arguments,
								MinSize:     dcl.Size{Height: 70},
								ToolTipText: "例如 -k {token} -s {server}；{key} 会替换为下方参数表的值",
							},

							dcl.Label{Text: "参数表"},
							dcl.TextEdit{
								AssignTo:    &d.txtArgsList,
								MinSize:     dcl.Size{Height: 80},
								ToolTipText: "每行一项，格式 key=value；命令行里的 {key} 即引用这里",
							},

							dcl.Label{Text: "停止命令"},
							dcl.LineEdit{
								AssignTo:    &d.txtStop,
								Text:        d.cfg.StopCommand,
								ToolTipText: "优雅停止参数，例如 --stop；留空则直接结束进程",
							},

							dcl.Label{Text: "停止超时 (ms)"},
							dcl.NumberEdit{
								AssignTo:           &d.numStop,
								Value:              float64(d.cfg.StopTimeout),
								MinValue:           500,
								MaxValue:           120000,
								SpinButtonsVisible: true,
							},
						},
					},

					// ---- 脚本执行 ----
					dcl.GroupBox{
						Title:  "运行方式（脚本）",
						Layout: dcl.VBox{Spacing: 3, Alignment: dcl.AlignHNearVNear},
						Children: []dcl.Widget{
							dcl.Composite{
								Layout: dcl.Grid{Columns: 2, Spacing: 6},
								Children: []dcl.Widget{
									dcl.Label{Text: "执行方式"},
									dcl.ComboBox{
										AssignTo:              &d.cmbMode,
										Model:                 modeOptions,
										CurrentIndex:          modeIndex(d.cfg.ScriptMode),
										OnCurrentIndexChanged: func() { d.updateModeHint() },
									},
									dcl.Label{Text: "解释器路径"},
									dcl.LineEdit{
										AssignTo:    &d.txtInterp,
										Text:        d.cfg.Interpreter,
										ToolTipText: "可留空：自动从 PATH 查找 python/powershell/node 等",
									},
								},
							},
							dcl.Label{
								AssignTo:  &d.lblModeHint,
								Text:      "",
								TextColor: walk.RGB(100, 116, 139),
							},
						},
					},

					// ---- 行为 ----
					dcl.GroupBox{
						Title:  "行为",
						Layout: dcl.VBox{Spacing: 3, Alignment: dcl.AlignHNearVNear},
						Children: []dcl.Widget{
							dcl.CheckBox{
								AssignTo: &d.chkAdmin,
								Text:     "以管理员身份运行（需要 UAC，无法捕获输出）",
								Checked:  d.cfg.RunAsAdmin,
							},
							dcl.CheckBox{
								AssignTo: &d.chkAutoRestart,
								Text:     "崩溃后自动重启",
								Checked:  d.cfg.AutoRestart,
							},
							dcl.Composite{
								Layout: dcl.HBox{Spacing: 8, MarginsZero: true},
								Children: []dcl.Widget{
									dcl.Label{Text: "重启间隔 (ms)"},
									dcl.NumberEdit{
										AssignTo:           &d.numRestart,
										Value:              float64(d.cfg.RestartDelay),
										MinValue:           500,
										MaxValue:           600000,
										SpinButtonsVisible: true,
										MaxSize:            dcl.Size{Width: 140},
									},
								},
							},
							dcl.CheckBox{
								AssignTo: &d.chkAutoStart,
								Text:     "应用启动时自动运行",
								Checked:  d.cfg.AutoStart,
							},
						},
					},

					// ---- 定时执行 ----
					// 每个动作只有一个输入框：填 HH:MM 就是每天执行，
					// 填 5 段表达式就按 cron 执行，不再提供两个互相覆盖的字段。
					dcl.GroupBox{
						Title:  "定时执行",
						Layout: dcl.VBox{Spacing: 3, Alignment: dcl.AlignHNearVNear},
						Children: []dcl.Widget{
							dcl.Composite{
								Layout: dcl.Grid{Columns: 2, Spacing: 6},
								Children: []dcl.Widget{
									dcl.Label{Text: "启动时间"},
									dcl.Composite{
										Layout: dcl.HBox{Spacing: 4, MarginsZero: true},
										Children: []dcl.Widget{
											dcl.LineEdit{
												AssignTo:      &d.txtSchedStart,
												StretchFactor: 1,
												ToolTipText: "HH:MM（每天）或 cron 表达式，例如 08:30 或 0 8 * * 1-5；留空不启用",
											},
											dcl.PushButton{
												Text:      "示例…",
												MinSize:   dcl.Size{Width: 64},
												OnClicked: func() { d.pickPreset(true) },
											},
										},
									},
									dcl.Label{Text: "停止时间"},
									dcl.Composite{
										Layout: dcl.HBox{Spacing: 4, MarginsZero: true},
										Children: []dcl.Widget{
											dcl.LineEdit{
												AssignTo:      &d.txtSchedStop,
												StretchFactor: 1,
												ToolTipText: "HH:MM（每天）或 cron 表达式，例如 23:00 或 0 23 * * *；留空不启用",
											},
											dcl.PushButton{
												Text:      "示例…",
												MinSize:   dcl.Size{Width: 64},
												OnClicked: func() { d.pickPreset(false) },
											},
										},
									},
									dcl.Label{Text: "周期重启"},
									dcl.Composite{
										Layout: dcl.HBox{Spacing: 6, MarginsZero: true},
										Children: []dcl.Widget{
											dcl.NumberEdit{
												AssignTo:           &d.numRestartMin,
												Value:              float64(d.cfg.RestartEveryMin),
												MinValue:           0,
												MaxValue:           10080,
												SpinButtonsVisible: true,
												MaxSize:            dcl.Size{Width: 120},
											},
											dcl.Label{Text: "分钟（0 表示关闭）"},
										},
									},
								},
							},
							dcl.Label{
								AssignTo:  &d.lblCronHint,
								Text:      "",
								TextColor: walk.RGB(100, 116, 139),
							},
						},
					},

					// ---- 探测 ----
					dcl.GroupBox{
						Title:  "状态探测（可选）",
						Layout: dcl.VBox{Spacing: 6, Alignment: dcl.AlignHNearVNear},
						Children: []dcl.Widget{
							dcl.CheckBox{
								AssignTo:         &d.chkProbe,
								Text:             "启用状态探测（用独立进程执行查询命令）",
								Checked:          d.cfg.ProbeEnabled,
								OnCheckedChanged: func() { d.updateProbeEnabled() },
							},
							dcl.Composite{
								Layout: dcl.Grid{Columns: 2, Spacing: 6},
								Children: []dcl.Widget{
									dcl.Label{Text: "查询命令"},
									dcl.LineEdit{
										AssignTo:    &d.txtProbeCmd,
										Text:        d.cfg.ProbeCommand,
										ToolTipText: "例如 --info / status --json",
									},
									dcl.Label{Text: "输出格式"},
									dcl.ComboBox{
										AssignTo:     &d.cmbParser,
										Model:        parserOptions,
										CurrentIndex: parserIndex(d.cfg.ProbeParser),
									},
									dcl.Label{Text: "正常判断键"},
									dcl.LineEdit{
										AssignTo:    &d.txtOKKey,
										Text:        d.cfg.ProbeOKKey,
										ToolTipText: "KeyValue/Json 下代表状态的键，例如 Connection status",
									},
									dcl.Label{Text: "正常判断值"},
									dcl.LineEdit{
										AssignTo:    &d.txtOKValue,
										Text:        d.cfg.ProbeOKValue,
										ToolTipText: "该键等于此值时视为正常；留空表示非空即正常",
									},
									dcl.Label{Text: "探测间隔 (ms)"},
									dcl.NumberEdit{
										AssignTo:           &d.numInterval,
										Value:              float64(d.cfg.ProbeInterval),
										MinValue:           1000,
										MaxValue:           60000,
										SpinButtonsVisible: true,
									},
								},
							},
						},
					},
				},
			},
			dcl.Composite{
				Layout: dcl.HBox{Spacing: 8, MarginsZero: true},
				Children: []dcl.Widget{
					dcl.HSpacer{},
					dcl.PushButton{Text: "取消", OnClicked: func() { d.dlg.Cancel() }},
					dcl.PushButton{Text: "保存", OnClicked: func() { d.confirm() }},
				},
			},
		},
	}.Create(owner)
	if err != nil {
		return err
	}
	return nil
}

func (d *editDialog) initAfterCreate() {
	_ = d.txtArgsList.SetText(loadArgsText(d.cfg.Args))
	if d.cmbParser.CurrentIndex() < 0 {
		_ = d.cmbParser.SetCurrentIndex(0)
	}
	d.cmbParser.CurrentIndexChanged().Attach(func() { d.updateProbeEnabled() })
	d.updateProbeEnabled()

	if d.cmbMode.CurrentIndex() < 0 {
		_ = d.cmbMode.SetCurrentIndex(0)
	}
	d.cmbMode.CurrentIndexChanged().Attach(func() { d.updateModeHint() })
	d.updateModeHint()

	d.txtSchedStart.TextChanged().Attach(func() { d.updateCronHint() })
	d.txtSchedStop.TextChanged().Attach(func() { d.updateCronHint() })
	d.updateCronHint()
}

// updateCronHint 实时解释 cron 表达式，填错能立刻看到原因。
func (d *editDialog) updateCronHint() {
	if d.lblCronHint == nil {
		return
	}
	var notes []string
	for _, pair := range []struct {
		label string
		text  string
	}{
		{"启动", d.txtSchedStart.Text()},
		{"停止", d.txtSchedStop.Text()},
	} {
		text := strings.TrimSpace(pair.text)
		if text == "" {
			continue
		}
		desc, err := describeSchedule(text)
		if err != nil {
			notes = append(notes, pair.label+"无效："+err.Error())
			continue
		}
		notes = append(notes, pair.label+" "+desc)
	}
	if len(notes) == 0 {
		_ = d.lblCronHint.SetText("留空表示不启用。可填 HH:MM（每天）或 cron 表达式，" +
			"点「示例…」挑选常用规则。")
		return
	}
	_ = d.lblCronHint.SetText(strings.Join(notes, "　|　"))
}

// describeSchedule 把用户输入解释成中文，用于实时提示。
func describeSchedule(text string) (string, error) {
	if isClockExpression(text) {
		hour, minute, err := parseClock(text)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("每天 %02d:%02d", hour, minute), nil
	}
	spec, err := ParseCron(text)
	if err != nil {
		return "", err
	}
	return "cron：" + spec.Describe(), nil
}

// pickPreset 打开示例列表，选中后填入对应的 cron 输入框。
func (d *editDialog) pickPreset(forStart bool) {
	target := "停止"
	if forStart {
		target = "启动"
	}
	index, ok := showCronPresetDialog(d.dlg, target)
	if !ok || index < 0 || index >= len(cronPresets) {
		return
	}
	entry := cronPresets[index]
	if forStart {
		_ = d.txtSchedStart.SetText(entry.Expr)
	} else {
		_ = d.txtSchedStop.SetText(entry.Expr)
	}
	d.updateCronHint()
}

func parserIndex(parser string) int {
	switch strings.ToLower(parser) {
	case "keyvalue":
		return 1
	case "json":
		return 2
	default:
		return 0
	}
}

func parserName(index int) string {
	switch index {
	case 1:
		return "KeyValue"
	case 2:
		return "Json"
	default:
		return "Lines"
	}
}

// loadArgsText 把参数表渲染成每行 key=value 的文本，便于直接编辑。
func loadArgsText(items []ArgItem) string {
	var sb strings.Builder
	for _, item := range items {
		sb.WriteString(item.Key)
		sb.WriteString("=")
		sb.WriteString(item.Value)
		sb.WriteString("\r\n")
	}
	return sb.String()
}

// parseArgsText 解析 key=value 文本。值里允许出现 =，只有第一个 = 作为分隔。
func parseArgsText(text string) []ArgItem {
	items := []ArgItem{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.Index(line, "=")
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		if key == "" {
			continue
		}
		items = append(items, ArgItem{Key: key, Value: strings.TrimSpace(line[idx+1:])})
	}
	return items
}

// updateProbeEnabled 探测相关输入只在启用后可用。
func (d *editDialog) updateProbeEnabled() {
	enabled := d.chkProbe.Checked()
	keyValueMode := enabled && d.cmbParser.CurrentIndex() != 0
	d.txtProbeCmd.SetEnabled(enabled)
	d.cmbParser.SetEnabled(enabled)
	d.numInterval.SetEnabled(enabled)
	d.txtOKKey.SetEnabled(keyValueMode)
	d.txtOKValue.SetEnabled(keyValueMode)
}

// updateModeHint 根据执行方式给出提示，并决定解释器输入是否可用。
func (d *editDialog) updateModeHint() {
	mode := modeValue(d.cmbMode.CurrentIndex())
	needsInterpreter := mode != ModeDirect
	d.txtInterp.SetEnabled(needsInterpreter)
	if d.lblModeHint == nil {
		return
	}
	if mode == ModeAuto {
		label := modeForExtension(filepath.Ext(strings.TrimSpace(d.txtExe.Text())))
		_ = d.lblModeHint.SetText("按扩展名推断：" + label + "；不确定时保持默认即可")
		return
	}
	if !needsInterpreter {
		_ = d.lblModeHint.SetText("直接启动目标文件，不做任何包装")
		return
	}
	_ = d.lblModeHint.SetText("脚本会通过解释器启动；解释器路径留空时自动从 PATH 查找")
}

func (d *editDialog) browseExe() {
	dlg := new(walk.FileDialog)
	dlg.Title = "选择要管理的程序"
	dlg.Filter = "可执行文件与脚本 (*.exe;*.bat;*.cmd;*.ps1;*.py;*.js)|*.exe;*.bat;*.cmd;*.ps1;*.py;*.js|所有文件 (*.*)|*.*"
	if ok, err := dlg.ShowOpen(d.dlg); err != nil || !ok {
		return
	}
	_ = d.txtExe.SetText(dlg.FilePath)
	d.updateModeHint()
	if strings.TrimSpace(d.txtName.Text()) == "" || d.txtName.Text() == "新程序" {
		base := filepath.Base(dlg.FilePath)
		_ = d.txtName.SetText(strings.TrimSuffix(base, filepath.Ext(base)))
	}
}

func (d *editDialog) confirm() {
	if strings.TrimSpace(d.txtName.Text()) == "" {
		walk.MsgBox(d.dlg, "配置无效", "名称不能为空", walk.MsgBoxIconWarning)
		return
	}
	if strings.TrimSpace(d.txtExe.Text()) == "" {
		walk.MsgBox(d.dlg, "配置无效", "请选择要管理的程序", walk.MsgBoxIconWarning)
		return
	}
	if d.chkProbe.Checked() && strings.TrimSpace(d.txtProbeCmd.Text()) == "" {
		walk.MsgBox(d.dlg, "配置无效", "启用了状态探测，但没有填写查询命令",
			walk.MsgBoxIconWarning)
		return
	}
	// 启动/停止各只有一个输入框，这里按内容类型统一校验。
	if _, err := ParseSchedule(d.txtSchedStart.Text()); err != nil {
		walk.MsgBox(d.dlg, "配置无效", "启动时间填写有误："+err.Error()+
			"\n可填 08:30（每天）或 0 8 * * 1-5（cron）", walk.MsgBoxIconWarning)
		return
	}
	if _, err := ParseSchedule(d.txtSchedStop.Text()); err != nil {
		walk.MsgBox(d.dlg, "配置无效", "停止时间填写有误："+err.Error()+
			"\n可填 23:00（每天）或 0 23 * * *（cron）", walk.MsgBoxIconWarning)
		return
	}
	d.collect()
	d.accepted = true
	d.dlg.Accept()
}

func (d *editDialog) collect() {
	d.cfg.Name = strings.TrimSpace(d.txtName.Text())
	d.cfg.ExePath = strings.TrimSpace(d.txtExe.Text())
	d.cfg.WorkDir = strings.TrimSpace(d.txtWorkDir.Text())
	d.cfg.Arguments = d.txtArgs.Text()
	d.cfg.Args = parseArgsText(d.txtArgsList.Text())
	d.cfg.StopCommand = strings.TrimSpace(d.txtStop.Text())
	d.cfg.StopTimeout = int(d.numStop.Value())
	d.cfg.RunAsAdmin = d.chkAdmin.Checked()
	d.cfg.AutoRestart = d.chkAutoRestart.Checked()
	d.cfg.RestartDelay = int(d.numRestart.Value())
	d.cfg.AutoStart = d.chkAutoStart.Checked()

	d.cfg.ProbeEnabled = d.chkProbe.Checked()
	d.cfg.ProbeCommand = strings.TrimSpace(d.txtProbeCmd.Text())
	d.cfg.ProbeParser = parserName(d.cmbParser.CurrentIndex())
	d.cfg.ProbeOKKey = strings.TrimSpace(d.txtOKKey.Text())
	d.cfg.ProbeOKValue = strings.TrimSpace(d.txtOKValue.Text())
	d.cfg.ProbeInterval = int(d.numInterval.Value())

	d.cfg.ScriptMode = modeValue(d.cmbMode.CurrentIndex())
	d.cfg.Interpreter = strings.TrimSpace(d.txtInterp.Text())

	// 统一格式：HH:MM 规范成两位数，cron 原样保留（压缩多余空格）。
	d.cfg.ScheduleStart = normalizeScheduleText(d.txtSchedStart.Text())
	d.cfg.ScheduleStop = normalizeScheduleText(d.txtSchedStop.Text())
	d.cfg.CronStart = ""
	d.cfg.CronStop = ""
	d.cfg.RestartEveryMin = int(d.numRestartMin.Value())
}

// normalizeScheduleText 规范用户输入：HH:MM 补齐两位，cron 用单空格。
func normalizeScheduleText(text string) string {
	value := strings.TrimSpace(text)
	if value == "" {
		return ""
	}
	if isClockExpression(value) {
		return normalizeClock(value)
	}
	return strings.Join(strings.Fields(value), " ")
}
