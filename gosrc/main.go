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
	"github.com/lxn/win"
)

// maxLogUICchars 限制日志控件里保留的字符数，避免长跑后界面变卡。
const maxLogUICchars = 120000

// crashLog 把启动期错误写入文件，避免 GUI 程序闪退后无从排查。
// crashLog 把启动期错误写入文件，避免 GUI 程序闪退后无从排查。
// 使用 UTF-8 编码；用记事本或 VS Code 打开即可正常显示中文。
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

// insert 在列表末尾追加一行，不触碰已有行。
func (m *rowModel) insert(r *Runner) {
	m.mu.Lock()
	index := len(m.rows)
	m.rows = append(m.rows, &programRow{runner: r})
	m.mu.Unlock()
	m.PublishRowsInserted(index, index)
}

// replace 把指定位置的 Runner 换成新的，其余行保持不变。
func (m *rowModel) replace(index int, r *Runner) {
	m.mu.Lock()
	if index < 0 || index >= len(m.rows) {
		m.mu.Unlock()
		return
	}
	m.rows[index] = &programRow{runner: r}
	m.mu.Unlock()
	m.PublishRowChanged(index)
}

// removeAt 删除指定行。
func (m *rowModel) removeAt(index int) {
	m.mu.Lock()
	if index < 0 || index >= len(m.rows) {
		m.mu.Unlock()
		return
	}
	m.rows = append(m.rows[:index], m.rows[index+1:]...)
	m.mu.Unlock()
	m.PublishRowsRemoved(index, index)
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
	mw    *walk.MainWindow
	table *walk.TableView

	// 菜单动作。
	actionAdd     *walk.Action
	actionEdit    *walk.Action
	actionRemove  *walk.Action
	actionStart   *walk.Action
	actionStop    *walk.Action
	actionRestart *walk.Action

	lblTitle       *walk.Label
	lblState       *walk.Label
	lblInfo        *walk.Label
	lblMeta        *walk.Label
	probeContainer *walk.Composite
	lblProbeHint   *walk.Label
	logEdit        *walk.TextEdit
	btnClearLog    *walk.PushButton
	tabs           *walk.TabWidget
	statusPage     *walk.TabPage

	lblAdmin     *walk.Label
	lblStatusBar *walk.Label

	// 菜单动作：勾选状态需要与配置同步。
	actionAutoStart *walk.Action
	actionMinimize  *walk.Action

	tray       *walk.NotifyIcon
	trayShow   *walk.Action
	trayStart  *walk.Action
	trayStop   *walk.Action
	trayExit   *walk.Action

	model   *rowModel
	runners []*Runner
	current *Runner

	exiting       bool
	trayHintShown bool
	// windowSaveTimer 用于合并窗口位置变化后的写盘操作。
	windowSaveTimer *time.Timer
}

func main() {
	defer func() {
		if r := recover(); r != nil {
			crashLog("panic: %v\n%s", r, debug.Stack())
			walk.MsgBox(nil, "启动失败", fmt.Sprint(r), walk.MsgBoxIconError)
			os.Exit(1)
		}
	}()
	// 单实例：多开会让两个实例各自拉起同一批程序，造成端口冲突。
	// 提权重启时旧实例会先退出，这里等一下再尝试获取锁。
	if !acquireSingleInstanceWithRetry(3 * time.Second) {
		if activateExistingInstance() {
			crashLog("已有实例在运行，已激活其窗口")
		} else {
			walk.MsgBox(nil, windowTitle,
				"已经有一个 MultiPro 在运行，请使用任务栏或托盘中的窗口。",
				walk.MsgBoxIconInformation)
		}
		os.Exit(0)
	}
	defer releaseSingleInstance()

	w := &mainWindow{app: loadConfig()}
	if err := w.buildUI(); err != nil {
		crashLog("buildUI: %v", err)
		walk.MsgBox(nil, "启动失败", err.Error(), walk.MsgBoxIconError)
		os.Exit(1)
	}
	w.buildRunners()
	w.buildTray()

	// 恢复上次的窗口位置（首次启动或无有效记录时保持默认居中）。
	applyWindowState(w.mw, loadWindowState())

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
		Title:    windowTitle,
		Icon:     appIcon(),
		MinSize:  dcl.Size{Width: 940, Height: 640},
		Size:     dcl.Size{Width: 1060, Height: 720},
		Layout:   dcl.VBox{MarginsZero: true, SpacingZero: true},
		// 所有操作都放在菜单栏和列表右键菜单里，不再占用顶部空间。
		MenuItems: w.buildMenuItems(),
		Children: []dcl.Widget{
			dcl.HSplitter{
				StretchFactor: 1,
				HandleWidth:   4,
				Children: []dcl.Widget{
					// ---- 左侧：程序列表 ----
					dcl.Composite{
						Layout: dcl.VBox{Margins: dcl.Margins{Left: 4, Top: 4, Right: 2, Bottom: 4}, Spacing: 0},
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
									{Title: "名称", Width: 180},
									{Title: "状态", Width: 72},
									{Title: "PID", Width: 58},
									{Title: "类型", Width: 78},
									{Title: "定时", Width: 140},
								},
								OnCurrentIndexChanged: func() { w.onTableSelection() },
								OnItemActivated:       func() { w.editSelected() },
							},
						},
					},
					// ---- 右侧：标题条 + 选项卡 ----
					dcl.Composite{
						Layout: dcl.VBox{Margins: dcl.Margins{Left: 3, Top: 4, Right: 6, Bottom: 4}, Spacing: 4},
						Children: []dcl.Widget{
							// 单行标题：程序名、状态、PID/运行时长。
							// 不再占用独立分组，把高度留给下面的选项卡。
							dcl.Composite{
								Layout: dcl.HBox{
									Spacing:   10,
									Margins:   dcl.Margins{Left: 2, Top: 2, Right: 2, Bottom: 2},
									Alignment: dcl.AlignHNearVNear,
								},
								Children: []dcl.Widget{
									dcl.Label{
										AssignTo: &w.lblTitle,
										Text:     "未选择程序",
										Font:     dcl.Font{PointSize: 12, Bold: true},
									},
									dcl.Label{
										AssignTo: &w.lblState,
										Text:     "",
										Font:     dcl.Font{Bold: true},
									},
									dcl.HSpacer{},
									dcl.Label{
										AssignTo:  &w.lblInfo,
										Text:      "",
										TextColor: walk.RGB(71, 85, 105),
									},
								},
							},
							dcl.TabWidget{
								AssignTo:      &w.tabs,
								StretchFactor: 1,
								Pages: []dcl.TabPage{
									// ---- 日志页（默认显示）----
									{
										Title:  "运行日志",
										Layout: dcl.VBox{Spacing: 6},
										Children: []dcl.Widget{
											dcl.Composite{
												Layout: dcl.HBox{Spacing: 6},
												Children: []dcl.Widget{
													dcl.Label{
														AssignTo:  &w.lblMeta,
														Text:      "",
														TextColor: walk.RGB(100, 116, 139),
													},
													dcl.HSpacer{},
													dcl.PushButton{
														AssignTo:  &w.btnClearLog,
														Text:      "清空",
														MinSize:   dcl.Size{Width: 64},
														OnClicked: func() { w.clearCurrentLog() },
													},
													dcl.PushButton{
														Text:      "复制全部",
														MinSize:   dcl.Size{Width: 76},
														OnClicked: func() { w.copyCurrentLog() },
													},
												},
											},
											dcl.TextEdit{
												AssignTo:      &w.logEdit,
												ReadOnly:      true,
												HScroll:       true,
												VScroll:       true,
												MinSize:       dcl.Size{Height: 120},
												StretchFactor: 1,
												TextColor:     walk.RGB(226, 232, 240),
												Background:    dcl.SolidColorBrush{Color: walk.RGB(15, 23, 42)},
												Font:          dcl.Font{Family: "Consolas", PointSize: 9},
											},
										},
									},
									// ---- 状态页 ----
									{
										AssignTo: &w.statusPage,
										Title:    "运行状态",
										Layout:   dcl.VBox{Spacing: 6},
										Children: []dcl.Widget{
											dcl.Composite{
												Layout: dcl.HBox{Spacing: 6, Alignment: dcl.AlignHNearVNear},
												Children: []dcl.Widget{
													dcl.Label{
														Text:      "探测数据",
														Font:      dcl.Font{Bold: true},
														TextColor: walk.RGB(71, 85, 105),
													},
													dcl.HSpacer{},
													dcl.PushButton{
														Text:      "立即刷新",
														MinSize:   dcl.Size{Width: 80},
														OnClicked: func() { w.requestProbe(w.current) },
													},
												},
											},
											dcl.Composite{
												AssignTo: &w.probeContainer,
												MinSize:  dcl.Size{Height: 72},
												Layout:   dcl.Flow{Spacing: 6},
											},
											dcl.Label{
												AssignTo:  &w.lblProbeHint,
												Text:      "",
												TextColor: walk.RGB(148, 163, 184),
											},
											dcl.VSpacer{},
										},
									},
								},
							},
						},
					},
				},
			},
			// ---- 状态栏 ----
			// 只保留一行文字信息，勾选项移到菜单栏，避免底部占高。
			dcl.Composite{
				MaxSize: dcl.Size{Height: 24},
				MinSize: dcl.Size{Height: 24},
				Layout:  dcl.HBox{Spacing: 10, Margins: dcl.Margins{Left: 8, Top: 3, Right: 8, Bottom: 3}},
				Children: []dcl.Widget{
					dcl.Label{AssignTo: &w.lblStatusBar, Text: "共 0 个程序"},
					dcl.HSpacer{},
					dcl.Label{AssignTo: &w.lblAdmin, Text: adminText()},
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
	w.mw.SizeChanged().Attach(func() {
		w.updateStatusBar()
		w.scheduleWindowStateSave()
	})
	w.mw.BoundsChanged().Attach(func() {
		w.scheduleWindowStateSave()
	})

	// 默认停留在「运行日志」页；状态页排在第二个。
	_ = w.tabs.SetCurrentIndex(0)

	w.buildTableContextMenu()
	return nil
}

// buildTableContextMenu 给程序列表挂上右键菜单。
//
// TableView 右键不会自动选中命中的行，这里先命中测试再手动选中，
// 避免菜单操作作用到上一次选中的程序上。
func (w *mainWindow) buildTableContextMenu() {
	if w.table == nil {
		return
	}
	menu, err := walk.NewMenu()
	if err != nil {
		return
	}

	add := walk.NewAction()
	_ = add.SetText("添加新程序…")
	add.Triggered().Attach(func() { w.addProgram() })
	_ = menu.Actions().Add(add)

	_ = menu.Actions().Add(walk.NewSeparatorAction())

	edit := walk.NewAction()
	_ = edit.SetText("编辑…")
	edit.Triggered().Attach(func() { w.editSelected() })
	_ = menu.Actions().Add(edit)

	remove := walk.NewAction()
	_ = remove.SetText("删除")
	remove.Triggered().Attach(func() { w.removeSelected() })
	_ = menu.Actions().Add(remove)

	_ = menu.Actions().Add(walk.NewSeparatorAction())

	start := walk.NewAction()
	_ = start.SetText("启动")
	start.Triggered().Attach(func() { w.startSelected() })
	_ = menu.Actions().Add(start)

	stop := walk.NewAction()
	_ = stop.SetText("停止")
	stop.Triggered().Attach(func() { w.stopSelected() })
	_ = menu.Actions().Add(stop)

	restart := walk.NewAction()
	_ = restart.SetText("重启")
	restart.Triggered().Attach(func() { w.restartSelected() })
	_ = menu.Actions().Add(restart)

	// 右键时按命中的行切换选中项；空白处右键则清空选择。
	w.table.MouseDown().Attach(func(x, y int, button walk.MouseButton) {
		if button != walk.RightButton {
			return
		}
		index := w.table.IndexAt(x, y)
		if index >= 0 {
			_ = w.table.SetCurrentIndex(index)
		}
		hasSelection := w.current != nil
		running := hasSelection && w.current.State() == StateRunning
		stopped := hasSelection && w.current.State() == StateStopped
		edit.SetEnabled(hasSelection)
		remove.SetEnabled(hasSelection)
		start.SetEnabled(stopped)
		stop.SetEnabled(running)
		restart.SetEnabled(hasSelection)
	})

	w.table.SetContextMenu(menu)
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

// makeRunner 创建一个带事件回调的 Runner。
func (w *mainWindow) makeRunner(cfg *ProgramConfig) *Runner {
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
	return r
}

// buildRunners 全量重建。只在启动时使用；编辑/新增/删除必须走增量方法，
// 否则会把所有正在运行的程序一起杀掉。
func (w *mainWindow) buildRunners() {
	for _, r := range w.runners {
		r.Kill()
	}
	w.runners = nil

	for _, cfg := range w.app.Programs {
		w.runners = append(w.runners, w.makeRunner(cfg))
	}
	w.model.rebuild(w.runners)
	if len(w.runners) > 0 {
		_ = w.table.SetCurrentIndex(0)
	}
}

// runnerIndex 返回 Runner 在切片中的位置。
func (w *mainWindow) runnerIndex(r *Runner) int {
	for i, item := range w.runners {
		if item == r {
			return i
		}
	}
	return -1
}

// replaceRunner 只替换指定 Runner，其他程序完全不受影响。
func (w *mainWindow) replaceRunner(old *Runner, cfg *ProgramConfig) *Runner {
	index := w.runnerIndex(old)
	if index < 0 {
		return nil
	}
	old.Kill()
	replacement := w.makeRunner(cfg)
	w.runners[index] = replacement
	w.model.replace(index, replacement)
	return replacement
}

// appendRunner 新增一个程序，已有程序保持原状。
func (w *mainWindow) appendRunner(cfg *ProgramConfig) *Runner {
	r := w.makeRunner(cfg)
	w.runners = append(w.runners, r)
	w.model.insert(r)
	return r
}

// removeRunner 删除一个程序，其他程序保持原状。
func (w *mainWindow) removeRunner(target *Runner) {
	index := w.runnerIndex(target)
	if index < 0 {
		return
	}
	target.Kill()
	w.runners = append(w.runners[:index], w.runners[index+1:]...)
	w.model.removeAt(index)
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
	// 只有用户本来就在底部时才跟随滚动；
	// 向上翻看历史日志时不要把它拽回去。
	firstVisible := w.logEdit.SendMessage(win.EM_GETFIRSTVISIBLELINE, 0, 0)
	lineCount := w.logEdit.SendMessage(win.EM_GETLINECOUNT, 0, 0)
	visibleLines := 0
	if bounds := w.logEdit.BoundsPixels(); bounds.Height > 0 {
		visibleLines = int(bounds.Height) / 18
	}
	atBottom := visibleLines > 0 &&
		int(lineCount)-int(firstVisible) <= visibleLines+1

	w.logEdit.AppendText(time.Now().Format("15:04:05") + "  " + line + "\r\n")

	// 控件里的文本会随运行时间无限增长，长跑之后滚动和重绘都会变慢。
	// 超过上限时删掉前 1/4，保留最近的内容。
	if length := w.logEdit.TextLength(); length > maxLogUICchars {
		keep := length - maxLogUICchars/2
		w.logEdit.SetTextSelection(0, keep)
		w.logEdit.ReplaceSelectedText("", false)
	}

	if atBottom {
		w.logEdit.SendMessage(win.EM_SETSEL, 0x7FFFFFFF, 0x7FFFFFFF)
		w.logEdit.SendMessage(win.EM_SCROLLCARET, 0, 0)
	}
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
	_ = w.lblInfo.SetText("点击「添加」创建第一个被管理的程序")
	_ = w.lblMeta.SetText("")
	w.clearProbeFields()
	if w.lblProbeHint != nil {
		_ = w.lblProbeHint.SetText("")
	}
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
// describeProgram 生成日志页顶部的一行摘要。完整路径已由悬停提示给出，
// 这里只放最需要快速确认的信息，避免占满一行。
func (w *mainWindow) describeProgram(cfg *ProgramConfig) string {
	parts := make([]string, 0, 4)
	label := cfg.ScriptLabel()
	if cfg.NeedsInterpreter() {
		label += "（解释器）"
	}
	parts = append(parts, label)
	if summary := w.current.ScheduleSummary(); summary != "" {
		parts = append(parts, "定时 "+summary)
	}
	if cfg.ProbeEnabled {
		parts = append(parts, fmt.Sprintf("探测 %d 秒", cfg.ProbeInterval/1000))
	}
	if cfg.AutoRestart {
		parts = append(parts, "崩溃重启")
	}
	return strings.Join(parts, " · ")
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

// setButtonsEnabled 现在改为刷新菜单项可用性；保留函数名以减少调用点改动。
func (w *mainWindow) setButtonsEnabled(canStart, canStop, hasSelection bool) {
	w.updateMenuEnabled()
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
	w.setStatusTabTitle(0)
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
	w.updateProbeHint(result)
}

// updateProbeHint 在状态页底部说明当前探测情况，避免空白页让人困惑。
func (w *mainWindow) updateProbeHint(result *ProbeResult) {
	if w.lblProbeHint == nil {
		return
	}
	if w.current == nil {
		_ = w.lblProbeHint.SetText("")
		return
	}
	cfg := w.current.Config()
	switch {
	case !cfg.ProbeEnabled:
		_ = w.lblProbeHint.SetText("该程序未启用状态探测。" +
			"可在「编辑 → 状态探测」中配置查询命令，这里会显示解析结果。")
	case !w.current.IsRunning():
		_ = w.lblProbeHint.SetText("程序未运行，暂无探测数据。")
	case result == nil || len(result.Fields) == 0:
		_ = w.lblProbeHint.SetText("等待探测结果…（查询命令：" +
			cfg.ProbeCommand + "）")
	default:
		level := "未知"
		switch result.Level {
		case ProbeOK:
			level = "正常"
		case ProbeBad:
			level = "异常"
		}
		_ = w.lblProbeHint.SetText(fmt.Sprintf("状态：%s　已解析 %d 项　每 %d 秒刷新",
			level, len(result.Fields), cfg.ProbeInterval/1000))
	}
	w.setStatusTabTitle(probeFieldCount(result))
}

// probeFieldCount 返回状态页应展示的数据量，用于标题提示。
func probeFieldCount(result *ProbeResult) int {
	if result == nil {
		return 0
	}
	return len(result.Fields)
}

// setStatusTabTitle 在状态页标题上附带数据量，不切页也能看出有没有内容。
func (w *mainWindow) setStatusTabTitle(count int) {
	if w.statusPage == nil {
		return
	}
	title := "运行状态"
	if count > 0 {
		title = fmt.Sprintf("运行状态 (%d)", count)
	}
	_ = w.statusPage.SetTitle(title)
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
	// 只追加新行，不影响其他正在运行的程序。
	w.appendRunner(cfg)
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
	clearIconCache()

	// 只替换被编辑的这一个 Runner，其他程序保持运行。
	// 这里的程序若本来在运行，重启后才会应用新参数。
	previous := w.current
	wasRunning := previous.IsRunning()
	replacement := w.replaceRunner(previous, cfg)
	if replacement == nil {
		return
	}
	// 索引没变时 SetCurrentIndex 不会触发选中事件，
	// 必须显式把 current 指向新 Runner 并刷新详情。
	w.current = replacement
	w.selectByID(cfg.ID)
	w.current = replacement
	w.refreshDetailFull()
	if wasRunning {
		walk.MsgBox(w.mw, "提示",
			"该程序正在运行，已被停止；新配置将在下次启动时生效。\n其他程序未受影响。",
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
	for i, p := range w.app.Programs {
		if p.ID == cfg.ID {
			w.app.Programs = append(w.app.Programs[:i], w.app.Programs[i+1:]...)
			break
		}
	}
	w.saveConfig()
	clearIconCache()

	// 只移除这一行，其他程序继续运行。
	target := w.current
	w.current = nil
	w.removeRunner(target)
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

// ---------------------------------------------------------------- 菜单栏

// buildMenuItems 构建窗口菜单栏。原来工具栏上的所有操作都收进这里，
// 顶部因此不再占用任何高度。
func (w *mainWindow) buildMenuItems() []dcl.MenuItem {
	// 选中程序相关动作：没有选中时应禁用。
	selectionItems := []dcl.MenuItem{
		dcl.Action{
			AssignTo:    &w.actionEdit,
			Text:        "编辑…",
			OnTriggered: func() { w.editSelected() },
		},
		dcl.Action{
			AssignTo:    &w.actionRemove,
			Text:        "删除",
			OnTriggered: func() { w.removeSelected() },
		},
		dcl.Separator{},
		dcl.Action{
			AssignTo:    &w.actionStart,
			Text:        "启动",
			OnTriggered: func() { w.startSelected() },
		},
		dcl.Action{
			AssignTo:    &w.actionStop,
			Text:        "停止",
			OnTriggered: func() { w.stopSelected() },
		},
		dcl.Action{
			AssignTo:    &w.actionRestart,
			Text:        "重启",
			OnTriggered: func() { w.restartSelected() },
		},
	}

	return []dcl.MenuItem{
		dcl.Menu{
			Text: "程序(&P)",
			Items: append([]dcl.MenuItem{
				dcl.Action{
					AssignTo:    &w.actionAdd,
					Text:        "添加新程序…",
					Shortcut:    dcl.Shortcut{Modifiers: walk.ModControl, Key: walk.KeyN},
					OnTriggered: func() { w.addProgram() },
				},
				dcl.Separator{},
			}, selectionItems...),
		},
		dcl.Menu{
			Text: "批量(&B)",
			Items: []dcl.MenuItem{
				dcl.Action{Text: "启动全部", OnTriggered: func() { w.startAll() }},
				dcl.Action{Text: "停止全部", OnTriggered: func() { w.stopAll() }},
			},
		},
		dcl.Menu{
			Text: "选项(&O)",
			Items: []dcl.MenuItem{
				dcl.Action{
					AssignTo:  &w.actionAutoStart,
					Text:      "开机自动启动本管理器",
					Checkable: true,
					Checked:   w.app.AutoStartManager,
					OnTriggered: func() {
						w.toggleAutoStart(w.actionAutoStart.Checked())
					},
				},
				dcl.Action{
					AssignTo:  &w.actionMinimize,
					Text:      "关闭窗口时最小化到托盘",
					Checkable: true,
					Checked:   w.app.MinimizeToTray,
					OnTriggered: func() {
						w.toggleMinimize(w.actionMinimize.Checked())
					},
				},
				dcl.Separator{},
				dcl.Action{
					Text:        "打开配置目录",
					OnTriggered: func() { openConfigFolder() },
				},
			},
		},
		dcl.Action{
			Text:        "关于",
			OnTriggered: func() { w.showAbout() },
		},
	}
}

// showAbout 显示版本与基本信息。
func (w *mainWindow) showAbout() {
	walk.MsgBox(w.mw, "关于 MultiPro",
		"MultiPro 通用程序管理器\n\n"+
			"管理多个长驻程序的启动、停止与定时执行。\n"+
			"配置文件："+stateConfigPath()+"\n\n"+
			adminText(),
		walk.MsgBoxIconInformation)
}

// stateConfigPath 返回配置文件路径，取不到时给出占位说明。
func stateConfigPath() string {
	path, err := configPath()
	if err != nil {
		return "(不可用)"
	}
	return path
}

// updateMenuEnabled 根据当前选择与运行状态刷新菜单项的可用性。
func (w *mainWindow) updateMenuEnabled() {
	hasSelection := w.current != nil
	running := hasSelection && w.current.State() == StateRunning
	stopped := hasSelection && w.current.State() == StateStopped

	if w.actionEdit != nil {
		w.actionEdit.SetEnabled(hasSelection)
	}
	if w.actionRemove != nil {
		w.actionRemove.SetEnabled(hasSelection)
	}
	if w.actionStart != nil {
		w.actionStart.SetEnabled(stopped)
	}
	if w.actionStop != nil {
		w.actionStop.SetEnabled(running)
	}
	if w.actionRestart != nil {
		w.actionRestart.SetEnabled(hasSelection)
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
	// 退出前记录窗口位置，下次启动时恢复。
	saveWindowState(w.mw)

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
	saveWindowState(w.mw)
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
		w.updateProbeHint(nil)
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
	w.syncMenuChecks()
}

// toggleAutoStart 由菜单项触发。菜单是勾选式的，状态即当前值。
func (w *mainWindow) toggleAutoStart(enabled bool) {
	w.app.AutoStartManager = enabled
	if err := applyAutoStart(enabled); err != nil {
		walk.MsgBox(w.mw, "开机自启", err.Error(), walk.MsgBoxIconWarning)
		// 写入注册表失败时把菜单勾选恢复成实际状态。
		w.syncMenuChecks()
	}
	w.saveConfig()
}

func (w *mainWindow) toggleMinimize(enabled bool) {
	w.app.MinimizeToTray = enabled
	w.saveConfig()
	w.syncMenuChecks()
}

// syncMenuChecks 让菜单里的勾选状态与配置保持一致。
func (w *mainWindow) syncMenuChecks() {
	if w.actionAutoStart != nil {
		_ = w.actionAutoStart.SetChecked(w.app.AutoStartManager)
	}
	if w.actionMinimize != nil {
		_ = w.actionMinimize.SetChecked(w.app.MinimizeToTray)
	}
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

// scheduleWindowStateSave 在窗口位置变化后延迟保存。
//
// 拖动窗口会连续触发大量事件，直接写盘过于频繁；因此合并成
// 一次延迟写入。这样即使程序被强制结束（任务管理器、断电），
// 也能保留最近一次的位置。
func (w *mainWindow) scheduleWindowStateSave() {
	if w.windowSaveTimer != nil {
		w.windowSaveTimer.Stop()
	}
	w.windowSaveTimer = time.AfterFunc(800*time.Millisecond, func() {
		w.uiAsync(func() { saveWindowState(w.mw) })
	})
}
