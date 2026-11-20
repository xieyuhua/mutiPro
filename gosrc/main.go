package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lxn/walk"
	dcl "github.com/lxn/walk/declarative"
)

// crashLog 把启动期错误写入文件，避免 GUI 程序闪退后无从排查。
func crashLog(format string, args ...interface{}) {
	f, err := os.OpenFile(filepath.Join(os.TempDir(), "multipro-crash.log"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, time.Now().Format("2006-01-02 15:04:05 ")+format+"\n", args...)
}

// programRow 是列表模型里的一行；直接引用 Runner，刷新时读实时状态。
type programRow struct {
	runner *Runner
}

// rowModel 提供「名称 / 状态 / PID」三列。walk 的 TableModel 按行读值，
// 定时刷新时只需发布 ItemChanged。
type rowModel struct {
	walk.TableModelBase
	rows []*programRow

	mu sync.Mutex
}

func (m *rowModel) RowCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.rows)
}

func (m *rowModel) Value(row, col int) interface{} {
	m.mu.Lock()
	r := m.rows[row].runner
	m.mu.Unlock()

	switch col {
	case 0:
		return r.Config().Name
	case 1:
		return r.State().String()
	case 2:
		if pid := r.PID(); pid > 0 {
			return strconv.Itoa(pid)
		}
		return ""
	case 3:
		if label := r.Config().ScriptLabel(); label != "可执行文件" {
			return label
		}
		return ""
	case 4:
		return r.ScheduleSummary()
	}
	return ""
}

// Image 实现 walk.ImageProvider：列表首列显示目标程序自带的图标。
// 图标按 exe 路径缓存，避免每次重绘都读盘。
func (m *rowModel) Image(index int) interface{} {
	m.mu.Lock()
	if index < 0 || index >= len(m.rows) {
		m.mu.Unlock()
		return nil
	}
	r := m.rows[index].runner
	m.mu.Unlock()
	return programIcon(r.Config())
}

func (m *rowModel) runnerAt(index int) *Runner {
	m.mu.Lock()
	defer m.mu.Unlock()
	if index < 0 || index >= len(m.rows) {
		return nil
	}
	return m.rows[index].runner
}

func (m *rowModel) indexOf(r *Runner) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, row := range m.rows {
		if row.runner == r {
			return i
		}
	}
	return -1
}

// rebuild 用新的 Runner 列表替换模型内容。
func (m *rowModel) rebuild(runners []*Runner) {
	m.mu.Lock()
	m.rows = make([]*programRow, len(runners))
	for i, r := range runners {
		m.rows[i] = &programRow{runner: r}
	}
	m.mu.Unlock()
	m.PublishRowsReset()
}

func (m *rowModel) publishAll() {
	m.mu.Lock()
	count := len(m.rows)
	m.mu.Unlock()
	if count == 0 {
		return
	}
	m.PublishRowsChanged(0, count-1)
}

// mainWindow 持有全部界面控件和运行状态。
type mainWindow struct {
	app *AppConfig

	// 窗口与控件
	mw        *walk.MainWindow
	table     *walk.TableView
	btnStart  *walk.PushButton
	btnStop   *walk.PushButton
	btnRestart *walk.PushButton
	btnEdit   *walk.PushButton
	btnRemove *walk.PushButton

	lblTitle       *walk.Label
	lblState       *walk.Label
	lblInfo        *walk.Label
	lblMeta        *walk.Label
	probeContainer *walk.Composite
	logEdit        *walk.TextEdit
	btnClearLog    *walk.PushButton

	chkAutoStart *walk.CheckBox
	chkMinimize  *walk.CheckBox
	lblAdmin     *walk.Label
	lblStatusBar *walk.Label

	tray       *walk.NotifyIcon
	trayShow   *walk.Action
	trayStart  *walk.Action
	trayStop   *walk.Action
	trayExit   *walk.Action

	model   *rowModel
	runners []*Runner
	current *Runner

	exiting bool
	trayHintShown bool
}

func main() {
	defer func() {
		if r := recover(); r != nil {
			crashLog("panic: %v\n%s", r, debug.Stack())
			walk.MsgBox(nil, "启动失败", fmt.Sprint(r), walk.MsgBoxIconError)
			os.Exit(1)
		}
	}()
	w := &mainWindow{app: loadConfig()}
	if err := w.buildUI(); err != nil {
		crashLog("buildUI: %v", err)
		walk.MsgBox(nil, "启动失败", err.Error(), walk.MsgBoxIconError)
		os.Exit(1)
	}
	w.buildRunners()
	w.buildTray()

	w.applyAutoStartFromConfig()
	w.refreshDetail()

	if w.hasArg("--minimized") {
		// 开机自启：只留托盘。walk 里通过隐藏主窗口实现。
		w.mw.Hide()
	}
	w.startAutoPrograms()
	w.startTimers()

	w.mw.Run()
}

func (w *mainWindow) hasArg(flag string) bool {
	for _, arg := range os.Args[1:] {
		if arg == flag {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- UI 构建

func (w *mainWindow) buildUI() error {
	mw := &w.mw
	w.model = &rowModel{}

	err := dcl.MainWindow{
		AssignTo: mw,
		Title:    "MultiPro 通用程序管理器",
		Icon:     appIcon(),
		MinSize:  dcl.Size{Width: 940, Height: 640},
		Size:     dcl.Size{Width: 1060, Height: 720},
		Layout:   dcl.VBox{MarginsZero: true, SpacingZero: true},
		Children: []dcl.Widget{
			// ---- 工具栏 ----
			dcl.Composite{
				Layout: dcl.HBox{Spacing: 6, Margins: dcl.Margins{Left: 8, Top: 6, Right: 8, Bottom: 6}},
				Children: []dcl.Widget{
					dcl.PushButton{Text: "添加", MinSize: dcl.Size{Width: 76}, OnClicked: func() { w.addProgram() }},
					dcl.PushButton{AssignTo: &w.btnEdit, Text: "编辑", MinSize: dcl.Size{Width: 76}, OnClicked: func() { w.editSelected() }},
					dcl.PushButton{AssignTo: &w.btnRemove, Text: "删除", MinSize: dcl.Size{Width: 76}, OnClicked: func() { w.removeSelected() }},
					dcl.VSpacer{},
					dcl.PushButton{AssignTo: &w.btnStart, Text: "启动", MinSize: dcl.Size{Width: 76}, OnClicked: func() { w.startSelected() }},
					dcl.PushButton{AssignTo: &w.btnStop, Text: "停止", MinSize: dcl.Size{Width: 76}, OnClicked: func() { w.stopSelected() }},
					dcl.PushButton{AssignTo: &w.btnRestart, Text: "重启", MinSize: dcl.Size{Width: 76}, OnClicked: func() { w.restartSelected() }},
					dcl.PushButton{Text: "启动全部", MinSize: dcl.Size{Width: 86}, OnClicked: func() { w.startAll() }},
					dcl.PushButton{Text: "停止全部", MinSize: dcl.Size{Width: 86}, OnClicked: func() { w.stopAll() }},
				},
			},
			dcl.HSplitter{
				HandleWidth: 4,
				Children: []dcl.Widget{
					// ---- 左侧：程序列表 ----
					dcl.Composite{
						Layout: dcl.VBox{Margins: dcl.Margins{Left: 8, Top: 6, Right: 4, Bottom: 6}, Spacing: 6},
						Children: []dcl.Widget{
							dcl.TableView{
								AssignTo:                 &w.table,
								Model:                    w.model,
								AlternatingRowBG:         true,
								ColumnsOrderable:         false,
								MultiSelection:           false,
								LastColumnStretched:      true,
								NotSortableByHeaderClick: true,
								MinSize:                  dcl.Size{Width: 360},
								Columns: []dcl.TableViewColumn{
									{Title: "名称", Width: 190},
									{Title: "状态", Width: 76},
									{Title: "PID", Width: 62},
									{Title: "类型", Width: 84},
									{Title: "定时", Width: 150},
								},
								OnCurrentIndexChanged: func() { w.onTableSelection() },
								OnItemActivated:       func() { w.editSelected() },
							},
						},
					},
					// ---- 右侧：详情 ----
					dcl.Composite{
						Layout: dcl.VBox{Margins: dcl.Margins{Left: 4, Top: 6, Right: 8, Bottom: 6}, Spacing: 8},
						Children: []dcl.Widget{
							dcl.GroupBox{
								Title:  "程序详情",
								Layout: dcl.VBox{Spacing: 4},
								Children: []dcl.Widget{
									dcl.Label{
										AssignTo: &w.lblTitle,
										Text:     "未选择程序",
										Font:     dcl.Font{PointSize: 14, Bold: true},
									},
									dcl.Label{
										AssignTo: &w.lblState,
										Text:     "",
										Font:     dcl.Font{Bold: true},
									},
									dcl.Label{
										AssignTo: &w.lblInfo,
										Text:     "点击「添加」创建第一个被管理的程序。",
									},
									dcl.Label{
										AssignTo: &w.lblMeta,
										Text:     "",
										TextColor: walk.RGB(100, 116, 139),
									},
								},
							},
							dcl.GroupBox{
								Title:  "运行状态（可选探测）",
								Layout: dcl.VBox{},
								Children: []dcl.Widget{
									dcl.Composite{
										AssignTo: &w.probeContainer,
										MinSize:  dcl.Size{Height: 72},
										Layout:   dcl.Flow{Spacing: 6},
									},
								},
							},
							dcl.Composite{
								Layout: dcl.HBox{Spacing: 6},
								Children: []dcl.Widget{
									dcl.Label{Text: "运行日志", Font: dcl.Font{Bold: true}},
									dcl.HSpacer{},
									dcl.PushButton{
										AssignTo: &w.btnClearLog,
										Text:     "清空",
										MinSize:  dcl.Size{Width: 64},
										OnClicked: func() { w.clearCurrentLog() },
									},
									dcl.PushButton{
										Text:     "复制全部",
										MinSize:  dcl.Size{Width: 76},
										OnClicked: func() { w.copyCurrentLog() },
									},
								},
							},
							dcl.TextEdit{
								AssignTo:      &w.logEdit,
								ReadOnly:      true,
								HScroll:       true,
								VScroll:       true,
								MinSize:       dcl.Size{Height: 180},
								StretchFactor: 1,
								TextColor:     walk.RGB(226, 232, 240),
								Background:    dcl.SolidColorBrush{Color: walk.RGB(15, 23, 42)},
								Font:          dcl.Font{Family: "Consolas", PointSize: 9},
							},
						},
					},
				},
			},
			// ---- 状态栏 ----
			dcl.Composite{
				Layout: dcl.HBox{Spacing: 12, Margins: dcl.Margins{Left: 10, Top: 4, Right: 10, Bottom: 4}},
				Children: []dcl.Widget{
					dcl.Label{AssignTo: &w.lblStatusBar, Text: "共 0 个程序"},
					dcl.HSpacer{},
					dcl.CheckBox{
						AssignTo:         &w.chkAutoStart,
						Text:             "开机自启",
						OnCheckedChanged: func() { w.toggleAutoStart() },
					},
					dcl.CheckBox{
						AssignTo:         &w.chkMinimize,
						Text:             "关闭时最小化到托盘",
						OnCheckedChanged: func() { w.toggleMinimize() },
					},
					dcl.Label{AssignTo: &w.lblAdmin, Text: adminText()},
					dcl.PushButton{
						Text:      "配置目录",
						MinSize:   dcl.Size{Width: 76},
						OnClicked: func() { openConfigFolder() },
					},
				},
			},
		},
	}.Create()

	if err != nil {
	return err
	}

	w.mw.Closing().Attach(func(canceled *bool, reason walk.CloseReason) {
		w.onClosing(canceled)
	})
	w.mw.SizeChanged().Attach(func() { w.updateStatusBar() })
	return nil
}

// updateStatusBar 汇总全局状态，显示在窗口底部。
func (w *mainWindow) updateStatusBar() {
	if w.lblStatusBar == nil {
		return
	}
	running, scheduled := 0, 0
	for _, r := range w.runners {
		if r.State() == StateRunning {
			running++
		}
		if r.ScheduleSummary() != "" {
			scheduled++
		}
	}
	text := fmt.Sprintf("共 %d 个程序，运行中 %d 个", len(w.runners), running)
	if scheduled > 0 {
		text += fmt.Sprintf("，已设置定时 %d 个", scheduled)
	}
	_ = w.lblStatusBar.SetText(text)
}

func adminText() string {
	if isAdministrator() {
		return "管理员权限：是"
	}
	return "管理员权限：否（需要时可整体提权）"
}

// ---------------------------------------------------------------- Runner

func (w *mainWindow) buildRunners() {
	for _, r := range w.runners {
		r.Kill()
	}
	w.runners = nil

	for _, cfg := range w.app.Programs {
		r := newRunner(cfg)
		r.OnStateChanged = func(runner *Runner) {
			w.uiAsync(func() { w.onRunnerState(runner) })
		}
		r.OnLog = func(runner *Runner, line string) {
			w.uiAsync(func() { w.onRunnerLog(runner, line) })
		}
		r.OnCrashed = func(runner *Runner, code int) {
			w.uiAsync(func() { w.onRunnerCrash(runner, code) })
		}
		w.runners = append(w.runners, r)
	}
	w.model.rebuild(w.runners)
	if len(w.runners) > 0 {
		_ = w.table.SetCurrentIndex(0)
	}
}

// uiAsync 把后台 goroutine 的回调切到 UI 线程。
func (w *mainWindow) uiAsync(fn func()) {
	if w.mw == nil || w.mw.IsDisposed() {
		return
	}
	defer func() { recover() }()
	w.mw.Synchronize(fn)
}

func (w *mainWindow) onRunnerState(r *Runner) {
	w.model.publishAll()
	if r == w.current {
		w.refreshDetail()
	}
	w.updateTrayTooltip()
}

func (w *mainWindow) onRunnerLog(r *Runner, line string) {
	if r != w.current || w.logEdit == nil {
		return
	}
	w.appendLogLine(line)
}

// appendLogLine 追加一行日志。Windows 的 EDIT 控件只识别 \r\n 换行，
// 单独用 \n 会把整段日志挤成一行。
func (w *mainWindow) appendLogLine(line string) {
	if w.logEdit == nil {
		return
	}
	w.logEdit.AppendText(time.Now().Format("15:04:05") + "  " + line + "\r\n")
}

func (w *mainWindow) onRunnerCrash(r *Runner, code int) {
	if r == w.current {
		w.appendLogLine(fmt.Sprintf("=== 检测到异常退出（退出码 %d），已安排重启", code))
	}
	if w.tray != nil && !w.mw.Visible() {
		_ = w.tray.ShowWarning(r.Config().Name, "程序已退出，正在自动重启")
	}
}

func (w *mainWindow) runnerByID(id string) *Runner {
	for _, r := range w.runners {
		if r.Config().ID == id {
			return r
		}
	}
	return nil
}

// ---------------------------------------------------------------- 选择与详情

func (w *mainWindow) onTableSelection() {
	r := w.model.runnerAt(w.table.CurrentIndex())
	if r == nil {
		w.current = nil
		w.showEmptyDetail()
		return
	}
	if r == w.current {
		return
	}
	w.current = r
	w.refreshDetailFull()
}

func (w *mainWindow) showEmptyDetail() {
	_ = w.lblTitle.SetText("未选择程序")
	_ = w.lblState.SetText("")
	_ = w.lblInfo.SetText("点击「添加」创建第一个被管理的程序。")
	_ = w.lblMeta.SetText("")
	w.clearProbeFields()
	if w.logEdit != nil {
		_ = w.logEdit.SetText("")
	}
	w.setButtonsEnabled(false, false, false)
}

func (w *mainWindow) refreshDetailFull() {
	if w.current == nil {
		w.showEmptyDetail()
		return
	}
	cfg := w.current.Config()
	_ = w.lblTitle.SetText(cfg.Name)
	_ = w.lblMeta.SetText(w.describeProgram(cfg))
	// 切换程序时按 CRLF 重新加载整段日志，避免显示成一行。
	_ = w.logEdit.SetText(w.current.DumpLog())
	w.refreshDetail()
	w.requestProbe(w.current)
	w.updateStatusBar()
}

// describeProgram 汇总右栏的固定信息：路径、类型、定时、探测。
func (w *mainWindow) describeProgram(cfg *ProgramConfig) string {
	parts := []string{"路径：" + cfg.ExePath}
	label := cfg.ScriptLabel()
	if cfg.NeedsInterpreter() {
		label += "（通过解释器运行）"
	}
	parts = append(parts, "类型："+label)
	if summary := w.current.ScheduleSummary(); summary != "" {
		parts = append(parts, "定时："+summary)
	}
	if cfg.ProbeEnabled {
		parts = append(parts, fmt.Sprintf("探测：%s，每 %d 秒", cfg.ProbeParser, cfg.ProbeInterval/1000))
	}
	if cfg.AutoRestart {
		parts = append(parts, "崩溃自动重启")
	}
	return strings.Join(parts, "　|　")
}

func (w *mainWindow) refreshDetail() {
	if w.current == nil {
		w.setButtonsEnabled(false, false, false)
		return
	}
	state := w.current.State()
	_ = w.lblState.SetText("● " + state.String())
	switch state {
	case StateRunning:
		info := fmt.Sprintf("PID %d 　运行时长 %s", w.current.PID(), formatUptime(w.current.Uptime()))
		if w.current.Config().RunAsAdmin {
			info += " 　（管理员）"
		}
		_ = w.lblInfo.SetText(info)
	case StateStopped:
		_ = w.lblInfo.SetText("当前未运行")
	default:
		_ = w.lblInfo.SetText("…")
	}
	w.setButtonsEnabled(state == StateStopped, state == StateRunning, true)
}

// ---------------------------------------------------------------- 批量与日志操作

func (w *mainWindow) startAll() {
	for _, r := range w.runners {
		w.tryStart(r, true)
	}
}

func (w *mainWindow) stopAll() {
	for _, r := range w.runners {
		if r.State() == StateStopped {
			continue
		}
		go r.Stop()
	}
}

func (w *mainWindow) clearCurrentLog() {
	if w.current == nil {
		return
	}
	w.current.ClearLog()
	if w.logEdit != nil {
		_ = w.logEdit.SetText("")
	}
}

func (w *mainWindow) copyCurrentLog() {
	if w.current == nil || w.logEdit == nil {
		return
	}
	if err := walk.Clipboard().SetText(w.logEdit.Text()); err != nil {
		walk.MsgBox(w.mw, "复制失败", err.Error(), walk.MsgBoxIconWarning)
	}
}

func (w *mainWindow) setButtonsEnabled(canStart, canStop, hasSelection bool) {
	if w.btnStart == nil {
		return
	}
	w.btnStart.SetEnabled(hasSelection && canStart)
	w.btnStop.SetEnabled(hasSelection && canStop)
	w.btnRestart.SetEnabled(hasSelection)
	w.btnEdit.SetEnabled(hasSelection)
	w.btnRemove.SetEnabled(hasSelection)
	if w.btnClearLog != nil {
		w.btnClearLog.SetEnabled(hasSelection)
	}
}

func formatUptime(d time.Duration) string {
	if d <= 0 {
		return "-"
	}
	if d >= 24*time.Hour {
		days := int(d / (24 * time.Hour))
		return fmt.Sprintf("%d天 %02d:%02d", days, int(d.Hours())%24, int(d.Minutes())%60)
	}
	if d >= time.Hour {
		return fmt.Sprintf("%02d:%02d:%02d", int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60)
	}
	return fmt.Sprintf("%02d:%02d", int(d.Minutes()), int(d.Seconds())%60)
}

// ---------------------------------------------------------------- 探测展示

func (w *mainWindow) clearProbeFields() {
	if w.probeContainer == nil {
		return
	}
	w.probeContainer.SetSuspended(true)
	children := w.probeContainer.Children()
	for children.Len() > 0 {
		children.At(0).Dispose()
	}
	w.probeContainer.SetSuspended(false)
}

func (w *mainWindow) renderProbe(result *ProbeResult) {
	w.clearProbeFields()
	if w.probeContainer == nil || result == nil {
		return
	}
	container := w.probeContainer
	container.SetSuspended(true)
	defer container.SetSuspended(false)

	for i, field := range result.Fields {
		if i >= 40 {
			break
		}
		text := field.Value
		if field.Key != "" {
			text = field.Key + "：" + field.Value
		}
		label, err := walk.NewLabel(container)
		if err != nil {
			continue
		}
		_ = label.SetText(text)
		_ = label.SetMinMaxSize(walk.Size{Width: 210, Height: 0}, walk.Size{Width: 210, Height: 0})
		if result.Level == ProbeBad {
			label.SetTextColor(walk.RGB(220, 38, 38))
		}
	}
}

// ---------------------------------------------------------------- 动作

func (w *mainWindow) startSelected() {
	if w.current == nil {
		return
	}
	w.tryStart(w.current, false)
}

func (w *mainWindow) tryStart(r *Runner, silent bool) {
	if r.IsRunning() {
		return
	}
	if err := r.Start(AppDir(), false); err != nil {
		if !silent {
			walk.MsgBox(w.mw, "启动失败", err.Error(), walk.MsgBoxIconError)
		}
	}
	w.model.publishAll()
	if r == w.current {
		w.refreshDetail()
	}
}

func (w *mainWindow) stopSelected() {
	if w.current == nil || w.current.State() == StateStopped {
		return
	}
	r := w.current
	go r.Stop() // 停止可能等待超时，放到后台避免卡界面
}

func (w *mainWindow) restartSelected() {
	if w.current == nil {
		return
	}
	r := w.current
	go func() {
		if r.State() != StateStopped {
			r.Stop()
		}
		if err := r.Start(AppDir(), false); err != nil {
			msg := err.Error()
			w.uiAsync(func() {
				walk.MsgBox(w.mw, "重启失败", msg, walk.MsgBoxIconError)
			})
		}
	}()
}

func (w *mainWindow) addProgram() {
	template := newProgramConfig()
	cfg, ok := showProgramDialog(w.mw, template)
	if !ok {
		return
	}
	cfg.ID = newID()
	w.app.Programs = append(w.app.Programs, cfg)
	w.saveConfig()
	clearIconCache()
	w.buildRunners()
	w.selectByID(cfg.ID)
}

func (w *mainWindow) editSelected() {
	if w.current == nil {
		return
	}
	old := w.current.Config()
	cfg, ok := showProgramDialog(w.mw, old)
	if !ok {
		return
	}
	cfg.ID = old.ID
	for i, p := range w.app.Programs {
		if p.ID == old.ID {
			w.app.Programs[i] = cfg
			break
		}
	}
	w.saveConfig()

	wasRunning := w.current.IsRunning()
	if wasRunning {
		w.current.Stop()
	}
	clearIconCache()
	w.buildRunners()
	w.selectByID(cfg.ID)
	if wasRunning {
		walk.MsgBox(w.mw, "提示", "程序正在运行，新配置将在下次启动时生效；已为您停止程序。",
			walk.MsgBoxIconInformation)
	}
}

func (w *mainWindow) removeSelected() {
	if w.current == nil {
		return
	}
	cfg := w.current.Config()
	if walk.MsgBox(w.mw, "删除程序", "确定删除 "+cfg.Name+" 吗？",
		walk.MsgBoxYesNo|walk.MsgBoxIconQuestion) != walk.DlgCmdYes {
		return
	}
	w.current.Stop()
	for i, p := range w.app.Programs {
		if p.ID == cfg.ID {
			w.app.Programs = append(w.app.Programs[:i], w.app.Programs[i+1:]...)
			break
		}
	}
	w.current = nil
	w.saveConfig()
	w.buildRunners()
	if len(w.runners) == 0 {
		w.showEmptyDetail()
	} else {
		_ = w.table.SetCurrentIndex(0)
	}
}

func (w *mainWindow) selectByID(id string) {
	for i, r := range w.runners {
		if r.Config().ID == id {
			_ = w.table.SetCurrentIndex(i)
			return
		}
	}
}

func (w *mainWindow) saveConfig() {
	if err := w.app.save(); err != nil {
		walk.MsgBox(w.mw, "保存失败", err.Error(), walk.MsgBoxIconError)
	}
}

// ---------------------------------------------------------------- 托盘

func (w *mainWindow) buildTray() {
	tray, err := walk.NewNotifyIcon(w.mw)
	if err != nil {
		return
	}
	w.tray = tray

	_ = tray.SetIcon(trayIconImage())
	_ = tray.SetToolTip("MultiPro 程序管理器")
	_ = tray.SetVisible(true)

	tray.MouseUp().Attach(func(x, y int, button walk.MouseButton) {
		if button == walk.LeftButton {
			w.restoreFromTray()
		}
	})

	show := walk.NewAction()
	_ = show.SetText("显示主窗口")
	show.Triggered().Attach(func() { w.restoreFromTray() })
	w.trayShow = show

	startAll := walk.NewAction()
	_ = startAll.SetText("启动全部")
	startAll.Triggered().Attach(func() {
		for _, r := range w.runners {
			w.tryStart(r, true)
		}
	})
	w.trayStart = startAll

	stopAll := walk.NewAction()
	_ = stopAll.SetText("停止全部")
	stopAll.Triggered().Attach(func() {
		for _, r := range w.runners {
			go r.Stop()
		}
	})
	w.trayStop = stopAll

	sep1 := walk.NewSeparatorAction()
	sep2 := walk.NewSeparatorAction()

	exit := walk.NewAction()
	_ = exit.SetText("退出")
	exit.Triggered().Attach(func() { w.exitApplication() })
	w.trayExit = exit

	actions := tray.ContextMenu().Actions()
	_ = actions.Add(show)
	_ = actions.Add(sep1)
	_ = actions.Add(startAll)
	_ = actions.Add(stopAll)
	_ = actions.Add(sep2)
	_ = actions.Add(exit)

	w.updateTrayTooltip()
}

func (w *mainWindow) restoreFromTray() {
	w.mw.Show()
	w.mw.SetFocus()
}

func (w *mainWindow) updateTrayTooltip() {
	if w.tray == nil {
		return
	}
	running := 0
	for _, r := range w.runners {
		if r.State() == StateRunning {
			running++
		}
	}
	text := "MultiPro 程序管理器 - 无运行程序"
	if running > 0 {
		text = fmt.Sprintf("MultiPro | 运行中 %d/%d", running, len(w.runners))
	}
	_ = w.tray.SetToolTip(text)
}

func (w *mainWindow) exitApplication() {
	w.exiting = true
	running := 0
	for _, r := range w.runners {
		if r.IsRunning() {
			running++
		}
	}
	if running > 0 {
		if walk.MsgBox(w.mw, "退出", fmt.Sprintf("仍有 %d 个程序在运行，退出将全部停止。确定退出吗？", running),
			walk.MsgBoxYesNo|walk.MsgBoxIconQuestion) != walk.DlgCmdYes {
			w.exiting = false
			return
		}
	}
	for _, r := range w.runners {
		r.Kill()
	}
	if w.tray != nil {
		_ = w.tray.SetVisible(false)
		w.tray.Dispose()
		w.tray = nil
	}
	w.mw.Dispose()
}

func (w *mainWindow) onClosing(canceled *bool) {
	if w.exiting {
		return
	}
	if w.app.MinimizeToTray {
		*canceled = true
		w.mw.Hide()
		if !w.trayHintShown && w.app.ShowTrayHint && w.tray != nil {
			w.trayHintShown = true
			_ = w.tray.ShowInfo("MultiPro 仍在运行",
				"程序已最小化到托盘。右键托盘图标可退出。")
		}
		return
	}
	// 未启用托盘常驻：按退出处理。
	running := 0
	for _, r := range w.runners {
		if r.IsRunning() {
			running++
		}
	}
	if running > 0 {
		if walk.MsgBox(w.mw, "退出", "仍有程序在运行，退出将全部停止。确定退出吗？",
			walk.MsgBoxYesNo|walk.MsgBoxIconQuestion) != walk.DlgCmdYes {
			*canceled = true
			return
		}
	}
	w.exiting = true
	for _, r := range w.runners {
		r.Kill()
	}
}

// ---------------------------------------------------------------- 定时器

func (w *mainWindow) startTimers() {
	// 每秒刷新：运行状态列、PID、运行时长、探测与定时调度。
	uiTimer := time.NewTicker(time.Second)
	go func() {
		for range uiTimer.C {
			if w.mw.IsDisposed() {
				return
			}
			w.uiAsync(func() {
				w.model.publishAll()
				if w.current != nil {
					w.refreshDetail()
				}
				w.runSchedules()
				w.runProbes()
				w.updateStatusBar()
			})
		}
	}()
}

// runSchedules 检查每个程序的定时启动/停止与周期重启。
// 触发动作放在后台执行，避免停止操作阻塞界面。
func (w *mainWindow) runSchedules() {
	now := time.Now()
	for _, r := range w.runners {
		switch r.CheckSchedule(now) {
		case ActionStart:
			w.appendSchedLog(r, "到达定时启动时间")
			w.tryStart(r, true)
		case ActionStop:
			w.appendSchedLog(r, "到达定时停止时间")
			go r.Stop()
		case ActionRestart:
			w.appendSchedLog(r, "到达周期重启时间")
			go func(runner *Runner) {
				runner.Stop()
				if err := runner.Start(AppDir(), false); err != nil {
					w.uiAsync(func() {
						w.appendSchedLog(runner, "周期重启失败："+err.Error())
					})
				}
			}(r)
		}
	}
}

// appendSchedLog 把调度动作写进当前程序的日志页。
func (w *mainWindow) appendSchedLog(r *Runner, message string) {
	r.appendLog("=== " + message)
}

// runProbes 检查到期的探测，在后台执行，结果回到 UI 线程渲染。
func (w *mainWindow) runProbes() {
	for _, r := range w.runners {
		if !r.ProbeDue() {
			continue
		}
		r.MarkProbed()
		go func(runner *Runner) {
			result := runner.Probe(AppDir())
			if result == nil {
				return
			}
			w.uiAsync(func() {
				if runner == w.current {
					w.renderProbe(result)
				}
			})
		}(r)
	}
}

func (w *mainWindow) requestProbe(r *Runner) {
	if r == nil || !r.Config().ProbeEnabled || !r.IsRunning() {
		w.clearProbeFields()
		return
	}
	r.MarkProbed()
	go func(runner *Runner) {
		result := runner.Probe(AppDir())
		if result == nil {
			return
		}
		w.uiAsync(func() {
			if runner == w.current {
				w.renderProbe(result)
			}
		})
	}(r)
}

// ---------------------------------------------------------------- 自启

func (w *mainWindow) applyAutoStartFromConfig() {
	_ = applyAutoStart(w.app.AutoStartManager)
	if w.chkAutoStart != nil {
		w.chkAutoStart.SetChecked(w.app.AutoStartManager)
	}
	if w.chkMinimize != nil {
		w.chkMinimize.SetChecked(w.app.MinimizeToTray)
	}
}

func (w *mainWindow) toggleAutoStart() {
	w.app.AutoStartManager = w.chkAutoStart.Checked()
	if err := applyAutoStart(w.app.AutoStartManager); err != nil {
		walk.MsgBox(w.mw, "开机自启", err.Error(), walk.MsgBoxIconWarning)
	}
	w.saveConfig()
}

func (w *mainWindow) toggleMinimize() {
	w.app.MinimizeToTray = w.chkMinimize.Checked()
	w.saveConfig()
}

func (w *mainWindow) startAutoPrograms() {
	for _, r := range w.runners {
		if r.Config().AutoStart {
			w.tryStart(r, true)
		}
	}
}

// ---------------------------------------------------------------- 工具

func execCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	return cmd.Start()
}

// splitArgs 把用户写的命令行拆成参数列表。
// 对 Windows 程序来说 Go 会重新拼回原始命令行，因此这里只需简单按空白拆分。
func splitArgs(line string) []string {
	return strings.Fields(line)
}
