package main

import (
	"fmt"

	"github.com/lxn/walk"
	dcl "github.com/lxn/walk/declarative"
)

// showCronPresetDialog 展示常用 cron 示例。返回选中项下标。
func showCronPresetDialog(owner walk.Form, target string) (int, bool) {
	var dlg *walk.Dialog
	var table *walk.TableView
	var lblPreview *walk.Label
	selected := -1
	accepted := false

	model := newPresetModel()

	labels := make([]string, len(cronPresets))
	for i, p := range cronPresets {
		labels[i] = fmt.Sprintf("%-16s %-18s %s", p.Label, p.Expr, p.Note)
	}
	_ = labels

	err := dcl.Dialog{
		AssignTo: &dlg,
		Title:    "选择 cron 示例（" + target + "）",
		Icon:     appIcon(),
		MinSize:  dcl.Size{Width: 620, Height: 460},
		Size:     dcl.Size{Width: 680, Height: 520},
		Layout:   dcl.VBox{Spacing: 8},
		Children: []dcl.Widget{
			dcl.Label{
				Text: "双击一行直接应用；也可以在下方看到表达式的含义。",
			},
			dcl.TableView{
				AssignTo:                 &table,
				Model:                    model,
				AlternatingRowBG:         true,
				ColumnsOrderable:         false,
				MultiSelection:           false,
				LastColumnStretched:      true,
				NotSortableByHeaderClick: true,
				StretchFactor:            1,
				Columns: []dcl.TableViewColumn{
					{Title: "说明", Width: 150},
					{Title: "表达式", Width: 140},
					{Title: "备注", Width: 280},
				},
				OnCurrentIndexChanged: func() {
					index := table.CurrentIndex()
					selected = index
					if index >= 0 && index < len(cronPresets) {
						_ = lblPreview.SetText("含义：" + describePreset(cronPresets[index].Expr))
					}
				},
				OnItemActivated: func() {
					index := table.CurrentIndex()
					if index >= 0 {
						selected = index
						accepted = true
						dlg.Accept()
					}
				},
			},
			dcl.Label{
				AssignTo:  &lblPreview,
				Text:      "选择一行查看含义",
				TextColor: walk.RGB(100, 116, 139),
			},
			dcl.Composite{
				Layout: dcl.HBox{Spacing: 8, MarginsZero: true},
				Children: []dcl.Widget{
					dcl.HSpacer{},
					dcl.PushButton{Text: "取消", MinSize: dcl.Size{Width: 84},
						OnClicked: func() { dlg.Cancel() }},
					dcl.PushButton{Text: "使用", MinSize: dcl.Size{Width: 84},
						OnClicked: func() {
							if selected < 0 {
								walk.MsgBox(dlg, "提示", "请先选择一条示例",
									walk.MsgBoxIconWarning)
								return
							}
							accepted = true
							dlg.Accept()
						}},
				},
			},
		},
	}.Create(owner)
	if err != nil {
		walk.MsgBox(owner, "打开示例列表失败", err.Error(), walk.MsgBoxIconError)
		return -1, false
	}

	_ = table.SetCurrentIndex(0)
	dlg.Run()
	if !accepted {
		return -1, false
	}
	return selected, true
}

// describePreset 把 cron 表达式翻译成中文描述，失败时返回原始表达式。
func describePreset(expr string) string {
	spec, err := ParseCron(expr)
	if err != nil {
		return expr + "（无法解析：" + err.Error() + "）"
	}
	return spec.Describe()
}

// presetModel 为示例列表提供三列数据。
type presetModel struct {
	walk.TableModelBase
}

func newPresetModel() *presetModel { return &presetModel{} }

func (m *presetModel) RowCount() int { return len(cronPresets) }

func (m *presetModel) Value(row, col int) interface{} {
	if row < 0 || row >= len(cronPresets) {
		return ""
	}
	entry := cronPresets[row]
	switch col {
	case 0:
		return entry.Label
	case 1:
		return entry.Expr
	case 2:
		return entry.Note
	}
	return ""
}
