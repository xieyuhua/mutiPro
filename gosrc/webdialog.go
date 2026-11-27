package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/lxn/walk"
	dcl "github.com/lxn/walk/declarative"
)

// showWebDialog 是 Web 管理的设置窗口。
// 返回 true 表示用户保存了修改。
func showWebDialog(owner walk.Form, app *AppConfig, mw *mainWindow) bool {
	var dlg *walk.Dialog
	var chkEnabled *walk.CheckBox
	var numPort *walk.NumberEdit
	var txtPassword *walk.LineEdit
	var lblStatus *walk.Label
	var lblAddresses *walk.Label

	accepted := false

	// 打开对话框前确保已有密码，否则「显示密码」会是空的。
	password, generated := app.ensureWebPassword()
	if generated {
		_ = app.save()
	}

	refreshStatus := func() {
		if mw.web == nil {
			return
		}
		running, port, lastErr := mw.web.Status()
		switch {
		case running:
			_ = lblStatus.SetText(fmt.Sprintf("● 运行中，端口 %d", port))
		case lastErr != "":
			_ = lblStatus.SetText("● 未运行：" + lastErr)
		default:
			_ = lblStatus.SetText("● 未运行")
		}

		lines := []string{"http://127.0.0.1:" + strconv.Itoa(app.WebPort) + "/  （本机）"}
		for _, ip := range LocalAddresses() {
			lines = append(lines, "http://"+ip+":"+strconv.Itoa(app.WebPort)+"/  （局域网）")
		}
		_ = lblAddresses.SetText(strings.Join(lines, "\n"))
	}

	err := dcl.Dialog{
		AssignTo: &dlg,
		Title:    "Web 管理",
		Icon:     appIcon(),
		MinSize:  dcl.Size{Width: 620, Height: 480},
		Size:     dcl.Size{Width: 680, Height: 540},
		Layout:   dcl.VBox{Spacing: 8},
		Children: []dcl.Widget{
			dcl.GroupBox{
				Title:  "服务",
				Layout: dcl.VBox{Spacing: 6, Alignment: dcl.AlignHNearVNear},
				Children: []dcl.Widget{
					dcl.CheckBox{
						AssignTo: &chkEnabled,
						Text:     "启用 Web 管理（局域网内可用浏览器访问）",
						Checked:  app.WebEnabled,
					},
					dcl.Composite{
						Layout: dcl.Grid{Columns: 2, Spacing: 6},
						Children: []dcl.Widget{
							dcl.Label{Text: "监听端口"},
							dcl.NumberEdit{
								AssignTo:           &numPort,
								Value:              float64(app.WebPort),
								MinValue:           1024,
								MaxValue:           65535,
								SpinButtonsVisible: true,
								MaxSize:            dcl.Size{Width: 130},
							},
							dcl.Label{Text: "访问密码"},
							dcl.Composite{
								Layout: dcl.HBox{Spacing: 4, MarginsZero: true},
								Children: []dcl.Widget{
									dcl.LineEdit{
										AssignTo:      &txtPassword,
										Text:          password,
										ReadOnly:      true,
										StretchFactor: 1,
									},
									dcl.PushButton{
										Text:    "重置",
										MinSize: dcl.Size{Width: 64},
										OnClicked: func() {
											newPassword, err := mw.resetWebPassword()
											if err != nil {
												walk.MsgBox(dlg, "重置失败", err.Error(),
													walk.MsgBoxIconError)
												return
											}
											_ = txtPassword.SetText(newPassword)
											refreshStatus()
											walk.MsgBox(dlg, "密码已重置",
												"新密码：\n\n    "+newPassword+
													"\n\n所有已登录的浏览器需要重新登录。",
												walk.MsgBoxIconInformation)
										},
									},
								},
							},
						},
					},
					dcl.Label{
						AssignTo:  &lblStatus,
						Text:      "",
						TextColor: walk.RGB(100, 116, 139),
					},
				},
			},

			dcl.GroupBox{
				Title:  "访问地址",
				Layout: dcl.VBox{Spacing: 4, Alignment: dcl.AlignHNearVNear},
				Children: []dcl.Widget{
					dcl.Label{
						AssignTo: &lblAddresses,
						Text:     "",
					},
					dcl.Label{
						Text:      "手机或另一台电脑与本机在同一局域网时，用上面任一地址访问。",
						TextColor: walk.RGB(148, 163, 184),
					},
				},
			},

			dcl.GroupBox{
				Title:  "安全提示",
				Layout: dcl.VBox{Spacing: 3, Alignment: dcl.AlignHNearVNear},
				Children: []dcl.Widget{
					dcl.Label{Text: "· 使用 HTTP 明文传输，只应在可信的局域网内开启。"},
					dcl.Label{Text: "· 登录连续失败 5 次会锁定该来源 5 分钟。"},
					dcl.Label{Text: "· 需要在公网访问时，请通过反向代理加上 HTTPS。"},
				},
			},

			dcl.Composite{
				Layout: dcl.HBox{Spacing: 8, MarginsZero: true},
				Children: []dcl.Widget{
					dcl.HSpacer{},
					dcl.PushButton{Text: "取消", MinSize: dcl.Size{Width: 84},
						OnClicked: func() { dlg.Cancel() }},
					dcl.PushButton{Text: "保存", MinSize: dcl.Size{Width: 84},
						OnClicked: func() {
							portChanged := int(numPort.Value()) != app.WebPort
							app.WebPort = int(numPort.Value())
							app.WebEnabled = chkEnabled.Checked()
							_ = app.save()

							// 按开关状态与端口变化重启服务。
							if mw.web != nil {
								if !app.WebEnabled {
									mw.web.Stop()
								} else if portChanged {
									if err := mw.web.Restart(app.WebPort); err != nil {
										walk.MsgBox(dlg, "启动失败", err.Error(),
											walk.MsgBoxIconError)
										app.WebEnabled = false
										_ = app.save()
										refreshStatus()
										return
									}
								} else {
									running, _, _ := mw.web.Status()
									if !running {
										if err := mw.web.Start(app.WebPort); err != nil {
											walk.MsgBox(dlg, "启动失败", err.Error(),
												walk.MsgBoxIconError)
											app.WebEnabled = false
											_ = app.save()
											refreshStatus()
											return
										}
									}
								}
							}
							mw.syncWebMenu()
							accepted = true
							dlg.Accept()
						},
					},
				},
			},
		},
	}.Create(owner)
	if err != nil {
		walk.MsgBox(owner, "打开 Web 设置失败", err.Error(), walk.MsgBoxIconError)
		return false
	}

	refreshStatus()
	dlg.Run()
	return accepted
}
