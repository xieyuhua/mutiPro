package main

import (
	"os"
	"strings"
	"time"

	"github.com/lxn/walk"
	dcl "github.com/lxn/walk/declarative"
)

// notifyDialog 机器人通知与卡死检测的设置窗口。
type notifyDialog struct {
	cfg *NotifyConfig
	// hangCfg 是引用同一份 AppConfig 的指针字段，直接原地修改。
	hangDetect      *bool
	hangTimeoutMin  *int
	hangAutoRestart *bool

	dlg *walk.Dialog

	chkEnabled   *walk.CheckBox
	cmbPlatform  *walk.ComboBox
	txtWebhook   *walk.LineEdit
	txtSecret    *walk.LineEdit
	lblPlatformHint *walk.Label
	chkCrash     *walk.CheckBox
	chkStartFail *walk.CheckBox
	chkHung      *walk.CheckBox
	chkScheduled *walk.CheckBox
	chkLogTail   *walk.CheckBox
	numTailLines *walk.NumberEdit

	chkHang      *walk.CheckBox
	numHangMin   *walk.NumberEdit
	chkHangRestart *walk.CheckBox

	accepted bool
}

var platformOptions = []string{
	"企业微信群机器人",
	"钉钉群机器人",
}

func platformIndex(platform string) int {
	if strings.EqualFold(platform, RobotDingTalk) {
		return 1
	}
	return 0
}

func platformValue(index int) string {
	if index == 1 {
		return RobotDingTalk
	}
	return RobotWeCom
}

// showNotifyDialog 打开设置窗口。直接修改传入的配置对象。
func showNotifyDialog(owner walk.Form, app *AppConfig) bool {
	d := &notifyDialog{
		cfg:             &app.Notify,
		hangDetect:      &app.HangDetect,
		hangTimeoutMin:  &app.HangTimeoutMin,
		hangAutoRestart: &app.HangAutoRestart,
	}
	if err := d.build(owner); err != nil {
		walk.MsgBox(owner, "打开通知设置失败", err.Error(), walk.MsgBoxIconError)
		return false
	}
	d.initAfterCreate()
	d.dlg.Run()
	return d.accepted
}

func (d *notifyDialog) build(owner walk.Form) error {
	return dcl.Dialog{
		AssignTo: &d.dlg,
		Title:    "通知设置",
		Icon:     appIcon(),
		MinSize:  dcl.Size{Width: 640, Height: 520},
		Size:     dcl.Size{Width: 700, Height: 620},
		Layout:   dcl.VBox{Spacing: 8},
		Children: []dcl.Widget{
			// ---- 机器人 ----
			dcl.GroupBox{
				Title:  "群机器人",
				Layout: dcl.VBox{Spacing: 6, Alignment: dcl.AlignHNearVNear},
				Children: []dcl.Widget{
					dcl.CheckBox{
						AssignTo:         &d.chkEnabled,
						Text:             "启用机器人通知",
						Checked:          d.cfg.Enabled,
						OnCheckedChanged: func() { d.updateEnabled() },
					},
					dcl.Composite{
						Layout: dcl.Grid{Columns: 2, Spacing: 6},
						Children: []dcl.Widget{
							dcl.Label{Text: "平台"},
							dcl.ComboBox{
								AssignTo:              &d.cmbPlatform,
								Model:                 platformOptions,
								CurrentIndex:          platformIndex(d.cfg.Platform),
								OnCurrentIndexChanged: func() { d.updatePlatformHint() },
							},
							dcl.Label{Text: "Webhook 地址"},
							dcl.LineEdit{
								AssignTo:    &d.txtWebhook,
								Text:        d.cfg.Webhook,
								ToolTipText: "群机器人设置页里复制的完整地址，含 access_token",
							},
							dcl.Label{Text: "加签密钥"},
							dcl.LineEdit{
								AssignTo:    &d.txtSecret,
								Text:        d.cfg.Secret,
								ToolTipText: "仅钉钉需要；未启用加签时留空",
							},
						},
					},
					dcl.Label{
						AssignTo:  &d.lblPlatformHint,
						Text:      "",
						TextColor: walk.RGB(100, 116, 139),
					},
					dcl.Composite{
						Layout: dcl.HBox{Spacing: 6, MarginsZero: true},
						Children: []dcl.Widget{
							dcl.PushButton{
								Text:      "发送测试消息",
								MinSize:   dcl.Size{Width: 120},
								OnClicked: func() { d.sendTest() },
							},
							dcl.Label{
								Text:      "点击后检查机器人是否收到消息",
								TextColor: walk.RGB(148, 163, 184),
							},
						},
					},
				},
			},

			// ---- 事件 ----
			dcl.GroupBox{
				Title:  "通知哪些事件",
				Layout: dcl.VBox{Spacing: 4, Alignment: dcl.AlignHNearVNear},
				Children: []dcl.Widget{
					dcl.CheckBox{
						AssignTo: &d.chkStartFail,
						Text:     "启动失败（进程启动后 5 秒内非零退出）",
						Checked:  d.cfg.OnStartFailed,
					},
					dcl.CheckBox{
						AssignTo: &d.chkCrash,
						Text:     "运行中崩溃（异常退出）",
						Checked:  d.cfg.OnCrash,
					},
					dcl.CheckBox{
						AssignTo: &d.chkHung,
						Text:     "进程无响应（长时间没有日志输出）",
						Checked:  d.cfg.OnHung,
					},
					dcl.CheckBox{
						AssignTo: &d.chkScheduled,
						Text:     "定时任务触发（启动 / 停止 / 周期重启）",
						Checked:  d.cfg.OnScheduled,
					},
				},
			},

			// ---- 消息内容 ----
			dcl.GroupBox{
				Title:  "消息内容",
				Layout: dcl.VBox{Spacing: 4, Alignment: dcl.AlignHNearVNear},
				Children: []dcl.Widget{
					dcl.CheckBox{
						AssignTo:         &d.chkLogTail,
						Text:             "附带最近的日志",
						Checked:          d.cfg.IncludeLogTail,
						OnCheckedChanged: func() { d.updateEnabled() },
					},
					dcl.Composite{
						Layout: dcl.HBox{Spacing: 6, MarginsZero: true},
						Children: []dcl.Widget{
							dcl.Label{Text: "行数"},
							dcl.NumberEdit{
								AssignTo:           &d.numTailLines,
								Value:              float64(d.cfg.LogTailLines),
								MinValue:           1,
								MaxValue:           50,
								SpinButtonsVisible: true,
								MaxSize:            dcl.Size{Width: 100},
							},
							dcl.Label{
								Text:      "超出平台长度上限时会自动截断",
								TextColor: walk.RGB(148, 163, 184),
							},
						},
					},
				},
			},

			// ---- 卡死检测 ----
			dcl.GroupBox{
				Title:  "卡死检测",
				Layout: dcl.VBox{Spacing: 4, Alignment: dcl.AlignHNearVNear},
				Children: []dcl.Widget{
					dcl.CheckBox{
						AssignTo:         &d.chkHang,
						Text:             "全局默认启用卡死检测",
						Checked:          *d.hangDetect,
						OnCheckedChanged: func() { d.updateEnabled() },
					},
					dcl.Label{
						Text: "这里只是默认值；每个程序可在「编辑 → 卡死检测」里单独设为启用或关闭。",
						TextColor: walk.RGB(148, 163, 184),
					},
					dcl.Composite{
						Layout: dcl.HBox{Spacing: 6, MarginsZero: true},
						Children: []dcl.Widget{
							dcl.Label{Text: "超过"},
							dcl.NumberEdit{
								AssignTo:           &d.numHangMin,
								Value:              float64(*d.hangTimeoutMin),
								MinValue:           1,
								MaxValue:           1440,
								SpinButtonsVisible: true,
								MaxSize:            dcl.Size{Width: 100},
							},
							dcl.Label{Text: "分钟没有日志输出即判定无响应"},
						},
					},
					dcl.CheckBox{
						AssignTo: &d.chkHangRestart,
						Text:     "判定无响应后自动重启",
						Checked:  *d.hangAutoRestart,
					},
					dcl.Label{
						Text:      "提示：频繁输出日志的程序不受影响；完全静默的程序请关闭此项。",
						TextColor: walk.RGB(148, 163, 184),
					},
				},
			},

			dcl.Composite{
				Layout: dcl.HBox{Spacing: 8, MarginsZero: true},
				Children: []dcl.Widget{
					dcl.HSpacer{},
					dcl.PushButton{Text: "取消", MinSize: dcl.Size{Width: 84},
						OnClicked: func() { d.dlg.Cancel() }},
					dcl.PushButton{Text: "保存", MinSize: dcl.Size{Width: 84},
						OnClicked: func() { d.save() }},
				},
			},
		},
	}.Create(owner)
}

func (d *notifyDialog) initAfterCreate() {
	if d.cmbPlatform.CurrentIndex() < 0 {
		_ = d.cmbPlatform.SetCurrentIndex(0)
	}
	d.updatePlatformHint()
	d.updateEnabled()
}

// updatePlatformHint 说明当前平台的配置要点。
func (d *notifyDialog) updatePlatformHint() {
	if d.lblPlatformHint == nil {
		return
	}
	if platformValue(d.cmbPlatform.CurrentIndex()) == RobotDingTalk {
		_ = d.lblPlatformHint.SetText(
			"钉钉：群设置 → 智能群助手 → 添加机器人 → 自定义。启用加签时把密钥填到上方。")
		return
	}
	_ = d.lblPlatformHint.SetText(
		"企业微信：群聊 → 右键 → 管理聊天信息 → 群机器人 → 添加。地址形如 .../cgi-bin/webhook/send?key=xxx")
}

// updateEnabled 根据开关启用/禁用相关输入。
func (d *notifyDialog) updateEnabled() {
	enabled := d.chkEnabled.Checked()
	d.cmbPlatform.SetEnabled(enabled)
	d.txtWebhook.SetEnabled(enabled)
	d.txtSecret.SetEnabled(enabled && platformValue(d.cmbPlatform.CurrentIndex()) == RobotDingTalk)
	for _, box := range []*walk.CheckBox{
		d.chkCrash, d.chkStartFail, d.chkHung, d.chkScheduled,
	} {
		box.SetEnabled(enabled)
	}
	d.chkLogTail.SetEnabled(enabled)
	d.numTailLines.SetEnabled(enabled && d.chkLogTail.Checked())

	hang := d.chkHang.Checked()
	d.numHangMin.SetEnabled(hang)
	d.chkHangRestart.SetEnabled(hang)
}

// collect 把界面上的值写回配置。
func (d *notifyDialog) collect() {
	d.cfg.Enabled = d.chkEnabled.Checked()
	d.cfg.Platform = platformValue(d.cmbPlatform.CurrentIndex())
	d.cfg.Webhook = strings.TrimSpace(d.txtWebhook.Text())
	d.cfg.Secret = strings.TrimSpace(d.txtSecret.Text())

	d.cfg.OnCrash = d.chkCrash.Checked()
	d.cfg.OnStartFailed = d.chkStartFail.Checked()
	d.cfg.OnHung = d.chkHung.Checked()
	d.cfg.OnScheduled = d.chkScheduled.Checked()

	d.cfg.IncludeLogTail = d.chkLogTail.Checked()
	d.cfg.LogTailLines = int(d.numTailLines.Value())

	*d.hangDetect = d.chkHang.Checked()
	*d.hangTimeoutMin = int(d.numHangMin.Value())
	*d.hangAutoRestart = d.chkHangRestart.Checked()
}

func (d *notifyDialog) save() {
	d.collect()
	if d.cfg.Enabled {
		if d.cfg.Webhook == "" {
			walk.MsgBox(d.dlg, "配置不完整", "启用通知时必须填写 Webhook 地址。",
				walk.MsgBoxIconWarning)
			return
		}
		if !strings.HasPrefix(strings.ToLower(d.cfg.Webhook), "http") {
			walk.MsgBox(d.dlg, "地址格式不对", "Webhook 应以 http:// 或 https:// 开头。",
				walk.MsgBoxIconWarning)
			return
		}
	}
	d.accepted = true
	d.dlg.Accept()
}

// sendTest 用当前界面上的值发一条测试消息，便于确认地址是否正确。
// 这里不要求先保存，填完就能试。
func (d *notifyDialog) sendTest() {
	d.collect()
	if d.cfg.Webhook == "" {
		walk.MsgBox(d.dlg, "还没填地址", "请先填写 Webhook 地址。",
			walk.MsgBoxIconWarning)
		return
	}

	message := NotifyMessage{
		Event:      EventCrash,
		Program:    "测试消息",
		Detail:     "这是一条来自 MultiPro 的测试通知，收到即表示配置正确。",
		OccurredAt: time.Now(),
	}
	hostname, _ := os.Hostname()
	message.Hostname = hostname

	notifier := newNotifier()
	// 测试发送同步执行，直接给出结果。
	if err := notifier.Send(d.cfg, nil, message); err != nil {
		walk.MsgBox(d.dlg, "发送失败", err.Error(), walk.MsgBoxIconError)
		return
	}
	walk.MsgBox(d.dlg, "发送成功",
		"消息已发送，请到群聊里确认是否收到。", walk.MsgBoxIconInformation)
}
