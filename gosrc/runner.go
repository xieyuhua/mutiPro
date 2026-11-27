package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/windows"
)

// RunState 进程生命周期状态。
type RunState int

const (
	StateStopped RunState = iota
	StateStarting
	StateRunning
	StateStopping
)

func (s RunState) String() string {
	switch s {
	case StateRunning:
		return "运行中"
	case StateStarting:
		return "启动中…"
	case StateStopping:
		return "停止中…"
	default:
		return "已停止"
	}
}

// ExitInfo 描述一次进程退出的上下文。
type ExitInfo struct {
	ExitCode int
	// StartFailed 表示启动后很短时间就退出，通常意味着配置或环境有问题。
	StartFailed bool
	// Requested 表示是用户主动停止，不是异常。
	Requested bool
	// RunDuration 是本次运行的时长。
	RunDuration time.Duration
}

const maxLogChars = 200000

// procHandle 屏蔽「普通启动」和「提权启动」的差异，
// 两者都归结为：一个 PID、一个退出信号、一个退出码。
type procHandle struct {
	pid       int
	done      chan struct{}
	cmd       *exec.Cmd      // 普通启动时有值
	sysHandle windows.Handle // 提权启动时有值
	// 提权启动时，cmd 包装进程把目标程序的输出写入 logFile。
	// MultiPro 轮询该文件来转发日志。
	logFile   string
	logOffset int64
}

// Runner 托管一个外部程序的进程生命周期。
// 所有方法都可以在后台 goroutine 里调用；事件回调也在后台触发，
// 由 UI 层切回主线程。
type Runner struct {
	mu   sync.Mutex
	cfg  *ProgramConfig
	proc *procHandle
	log  strings.Builder
	// logFile 按天把输出写入磁盘；为 nil 时不写文件。
	logFile *logWriter

	state       RunState
	stopWanted  bool
	disposed    bool
	lastProbeAt time.Time
	probeBusy   bool
	restartGen  int
	startedAt   time.Time
	// lastRestartCycle 记录上一轮周期重启的起点，配合 RestartEveryMin 计时。
	lastRestartCycle time.Time
	// 卡死检测：最近一次收到输出的时间。
	lastOutputAt time.Time
	// 已经就卡死发过通知，避免持续重复提醒。
	hangReported bool
	// 定时任务上次触发的时刻，避免同一分钟内重复触发。
	lastSchedStart time.Time
	lastSchedStop  time.Time
	// 定时规则解析结果缓存，配置变化时通过 schedCacheKey 失效。
	startSched    *Schedule
	stopSched     *Schedule
	schedCacheKey string

	OnStateChanged func(*Runner)
	OnLog          func(*Runner, string)
	OnCrashed      func(*Runner, int)
	// OnExit 在进程退出时总是触发（无论是否开启了自动重启）。
	// 与 OnCrashed 的区别：OnCrashed 只在安排了重启时触发，
	// 这里用于通知类场景，需要覆盖「启动即失败」等所有异常退出。
	OnExit func(*Runner, ExitInfo)
}

func newRunner(cfg *ProgramConfig) *Runner {
	return &Runner{cfg: cfg, state: StateStopped}
}

func (r *Runner) Config() *ProgramConfig {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cfg
}

func (r *Runner) updateConfig(cfg *ProgramConfig) {
	r.mu.Lock()
	r.cfg = cfg
	r.mu.Unlock()
}

func (r *Runner) State() RunState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state
}

func (r *Runner) IsRunning() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.proc != nil
}

func (r *Runner) PID() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.proc == nil {
		return 0
	}
	return r.proc.pid
}

func (r *Runner) Uptime() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.proc == nil || r.startedAt.IsZero() {
		return 0
	}
	return time.Since(r.startedAt)
}

// DumpLog 返回完整日志，使用 Windows 编辑控件要求的 CRLF 换行。
// 内存里按 \n 存储更省空间，输出时统一转换；否则整段日志会挤成一行。
func (r *Runner) DumpLog() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return toCRLF(r.log.String())
}

// toCRLF 把裸 \n 转成 \r\n，已经是 \r\n 的不重复转换。
func toCRLF(text string) string {
	if !strings.Contains(text, "\n") {
		return text
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.ReplaceAll(text, "\n", "\r\n")
}

func (r *Runner) ClearLog() {
	r.mu.Lock()
	r.log.Reset()
	r.mu.Unlock()
}

func (r *Runner) stateCallback() func(*Runner) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.OnStateChanged
}

func (r *Runner) setState(next RunState) {
	r.mu.Lock()
	changed := r.state != next
	r.state = next
	callback := r.OnStateChanged
	r.mu.Unlock()
	if changed && callback != nil {
		callback(r)
	}
}

func (r *Runner) appendLog(line string) {
	if strings.TrimSpace(line) == "" {
		return
	}
	r.mu.Lock()
	r.log.WriteString(line)
	r.log.WriteByte('\n')
	if r.log.Len() > maxLogChars {
		trimLogByLines(&r.log)
	}
	callback := r.OnLog
	writer := r.logFile
	r.mu.Unlock()

	// 文件写入放在锁外，避免慢磁盘拖住状态查询和界面刷新。
	if writer != nil {
		writer.Write(line)
	}
	if callback != nil {
		callback(r, line)
	}
}

// SetLogFile 启停日志文件写入。运行中切换会立即生效。
func (r *Runner) SetLogFile(enabled bool, keepDays int) {
	r.mu.Lock()
	old := r.logFile
	r.mu.Unlock()

	if old != nil {
		old.Close()
	}
	var writer *logWriter
	if enabled {
		writer = newLogWriter(r.Config().Name, keepDays, true)
	}

	r.mu.Lock()
	r.logFile = writer
	r.mu.Unlock()
}

// LogTail 返回最近 n 行非空日志，用于通知消息。
func (r *Runner) LogTail(n int) []string {
	r.mu.Lock()
	text := r.log.String()
	r.mu.Unlock()

	if n <= 0 {
		return nil
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	// 从后往前收集，保证取到的是最新内容。
	result := make([]string, 0, n)
	for i := len(lines) - 1; i >= 0 && len(result) < n; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			result = append(result, line)
		}
	}
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	return result
}

// CheckHang 判断进程是否长时间没有输出。
//
// 返回 true 表示本轮刚判定为卡死（同一轮卡死只返回一次），
// 调用方据此发送通知并按需重启。进程已停止时返回 false。
func (r *Runner) CheckHang(timeout time.Duration, now time.Time) bool {
	if timeout <= 0 {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.proc == nil || r.lastOutputAt.IsZero() || r.hangReported {
		return false
	}
	if now.Sub(r.lastOutputAt) < timeout {
		return false
	}
	r.hangReported = true
	return true
}

// MarkHangRestarted 在因卡死重启后重置状态，让下一轮检测重新计时。
func (r *Runner) MarkHangRestarted() {
	r.mu.Lock()
	r.hangReported = false
	r.lastOutputAt = time.Now()
	r.mu.Unlock()
}

// CloseLogFile 关闭日志文件句柄（退出时调用）。
func (r *Runner) CloseLogFile() {
	r.mu.Lock()
	writer := r.logFile
	r.logFile = nil
	r.mu.Unlock()
	if writer != nil {
		writer.Close()
	}
}

// trimLogByLines 丢弃前半段日志，保留后半段。
//
// 按行边界切分而不是按字节：中文是多字节 UTF-8，直接按字节切
// 会从字符中间截断，留下乱码。同时保证不会把一行切成两半。
func trimLogByLines(log *strings.Builder) {
	text := log.String()
	half := len(text) / 2
	// 从目标位置往后找最近的换行，确保从行首开始保留。
	if idx := strings.IndexByte(text[half:], '\n'); idx >= 0 {
		half += idx + 1
	} else {
		// 找不到换行说明只有一行超长内容，退化为按字节截断，
		// 但要把切点回退到 UTF-8 字符边界。
		half = utf8SafeCut(text, half)
	}
	log.Reset()
	log.WriteString(text[half:])
}

// utf8SafeCut 把切点回退到合法的 UTF-8 字符起始位置。
func utf8SafeCut(text string, cut int) int {
	if cut <= 0 {
		return 0
	}
	if cut >= len(text) {
		return len(text)
	}
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return cut
}

// Start 启动程序。fromRestart 为 true 表示自动重启，此时不清除停止标记。
func (r *Runner) Start(baseDir string, fromRestart bool) error {
	r.mu.Lock()
	if r.disposed {
		r.mu.Unlock()
		return errors.New("已释放")
	}
	if r.proc != nil {
		r.mu.Unlock()
		return errors.New("程序已在运行")
	}
	if !fromRestart {
		r.stopWanted = false
	}
	cfg := r.cfg
	r.mu.Unlock()

	targetPath := cfg.resolveExePath(baseDir)
	if targetPath == "" || !isFile(targetPath) {
		return errors.New("找不到程序文件：" + targetPath)
	}
	workDir := cfg.resolveWorkDir(baseDir)
	if !isDir(workDir) {
		workDir = filepathDir(targetPath)
	}

	// 脚本按扩展名/配置选择解释器，exe 直接运行。
	launchExe, commandLine, err := cfg.BuildLaunch(baseDir)
	if err != nil {
		return err
	}

	r.setState(StateStarting)
	var handle *procHandle
	if cfg.RunAsAdmin {
		handle, err = startElevated(r, launchExe, commandLine, workDir)
	} else {
		handle, err = startNormal(r, launchExe, commandLine, workDir)
	}
	if err != nil {
		r.setState(StateStopped)
		return err
	}

	r.mu.Lock()
	r.proc = handle
	r.startedAt = time.Now()
	r.lastRestartCycle = time.Now()
	// 卡死检测以「最后一次输出」为基准，启动时重置。
	r.lastOutputAt = time.Now()
	r.hangReported = false
	r.mu.Unlock()

	if cfg.NeedsInterpreter() {
		r.appendLog("=== 已启动：" + targetPath + "（" + cfg.ScriptLabel() + "）")
		r.appendLog("=== 解释器：" + launchExe)
	} else {
		r.appendLog("=== 已启动：" + targetPath)
	}
	if commandLine != "" {
		r.appendLog("=== 参数：" + commandLine)
	}
	if cfg.RunAsAdmin && handle.logFile == "" {
		r.appendLog("=== 以管理员身份运行：无法捕获输出，日志页不会显示内容")
	}
	r.setState(StateRunning)

	go r.watch(handle)
	return nil
}

// startNormal 用 exec.Cmd 启动，可重定向输出并精确等待退出。
func startNormal(r *Runner, exePath, commandLine, workDir string) (*procHandle, error) {
	cmd := exec.Command(exePath)
	cmd.Dir = workDir
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow: true,
		// Windows 的 CreateProcessW 用 CmdLine 解析被启动的程序，
		// 它必须以程序名开头。只传参数会导致系统把第一个参数
		// （例如 "-k svip666"）当作可执行文件路径去查找，
		// 程序表现为「显示帮助后退出」或「parameter -k not found」。
		// 因此这里始终拼上带引号的程序名前缀。
		CmdLine: quoteArg(exePath) + buildCmdLineSuffix(commandLine),
	}
	cmd.Stdout = &processOutput{runner: r}
	cmd.Stderr = &processOutput{runner: r}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &procHandle{pid: cmd.Process.Pid, done: make(chan struct{}), cmd: cmd}, nil
}

// buildCmdLineSuffix 把参数部分拼到程序名后面，保证中间有分隔空格。
func buildCmdLineSuffix(commandLine string) string {
	trimmed := strings.TrimSpace(commandLine)
	if trimmed == "" {
		return ""
	}
	// 程序名已带引号，参数前必须有一个空格分隔。
	if strings.HasPrefix(trimmed, " ") {
		return trimmed
	}
	return " " + trimmed
}

// startElevated 以管理员身份启动程序。
//
// 提权进程由系统创建，无法像普通子进程那样重定向标准输出。这里让 cmd.exe
// 做提权入口：cmd 用 runas 启动，内部把目标程序的 stdout/stderr 重定向到
// 日志文件，cmd 自身的退出码就是目标程序的退出码。MultiPro 轮询日志文件，
// 因此提权程序同样能看到输出，并能拿到真实退出码。
//
// cmd 不可用时退回直接 runas 启动，此时仍可跟踪进程，只是没有日志。
func startElevated(r *Runner, exePath, commandLine, workDir string) (*procHandle, error) {
	cmdPath, cmdErr := exec.LookPath("cmd.exe")
	if cmdErr != nil {
		return startElevatedDirect(exePath, commandLine, workDir)
	}

	logFile, err := elevationLogPath()
	if err != nil {
		return startElevatedDirect(exePath, commandLine, workDir)
	}

	// 路径和参数都加引号，避免空格与 & 等字符被 cmd 解释。
	target := quoteArg(exePath)
	if strings.TrimSpace(commandLine) != "" {
		target += " " + commandLine
	}
	// cmd 的 /c 参数整体加引号：/c ""prog" args > "log" 2>&1"
	inner := `/c ""` + target + `" > "` + logFile + `" 2>&1"`
	info, runErr := shellExecuteExRunAs(cmdPath, inner, workDir, true)
	if runErr != nil {
		os.Remove(logFile)
		return nil, runErr
	}

	pid, pidErr := windows.GetProcessId(windows.Handle(info.hProcess))
	if pidErr != nil {
		windows.CloseHandle(windows.Handle(info.hProcess))
		return nil, pidErr
	}
	return &procHandle{
		pid:       int(pid),
		done:      make(chan struct{}),
		sysHandle: windows.Handle(info.hProcess),
		logFile:   logFile,
	}, nil
}

// elevationLogPath 为一次提权启动生成唯一的日志文件路径。
func elevationLogPath() (string, error) {
	dir := filepath.Join(os.TempDir(), "MultiPro-elevated")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := "out-" + strconv.FormatInt(time.Now().UnixNano(), 36) + ".log"
	return filepath.Join(dir, name), nil
}

// startElevatedDirect 是 cmd 不可用时的兜底：直接 runas 启动目标程序。
func startElevatedDirect(exePath, commandLine, workDir string) (*procHandle, error) {
	info, err := shellExecuteExRunAs(exePath, commandLine, workDir, false)
	if err != nil {
		return nil, err
	}
	pid, pidErr := windows.GetProcessId(windows.Handle(info.hProcess))
	if pidErr != nil {
		windows.CloseHandle(windows.Handle(info.hProcess))
		return nil, pidErr
	}
	return &procHandle{
		pid:       int(pid),
		done:      make(chan struct{}),
		sysHandle: windows.Handle(info.hProcess),
	}, nil
}

// quoteArg 用双引号包裹参数，供命令行拼接使用。
func quoteArg(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

// watch 等待进程退出，处理退出码与自动重启。
func (r *Runner) watch(handle *procHandle) {
	startedAt := time.Now()

	// 提权程序的输出只能通过日志文件中转，退出前需要边等边转发。
	if handle.logFile != "" {
		finished := make(chan int, 1)
		go func() { finished <- r.waitExit(handle) }()
		ticker := time.NewTicker(400 * time.Millisecond)
		defer ticker.Stop()
		var exitCode int
	waitLoop:
		for {
			select {
			case exitCode = <-finished:
				break waitLoop
			case <-ticker.C:
				r.drainElevationLog(handle)
			}
		}
		// 收尾：把剩余输出读完，再清理临时文件。
		r.drainElevationLog(handle)
		cleanupElevationFiles(handle)
		r.finishWatch(handle, exitCode, startedAt)
		return
	}

	exitCode := r.waitExit(handle)
	r.finishWatch(handle, exitCode, startedAt)
}

// finishWatch 处理退出后的状态切换、自动重启与诊断提示。
func (r *Runner) finishWatch(handle *procHandle, exitCode int, startedAt time.Time) {
	close(handle.done)

	r.mu.Lock()
	isCurrent := r.proc == handle
	if isCurrent {
		r.proc = nil
		r.startedAt = time.Time{}
	}
	wantRestart := isCurrent && !r.stopWanted && !r.disposed && r.cfg.AutoRestart
	r.state = StateStopped
	delayMs := r.cfg.RestartDelay
	crashCallback := r.OnCrashed
	stateCallback := r.OnStateChanged
	exitCallback := r.OnExit
	stopWanted := r.stopWanted
	r.mu.Unlock()

	if !isCurrent {
		return
	}
	r.appendLog("=== 进程已退出，退出码 " + strconv.Itoa(exitCode))

	duration := time.Since(startedAt)
	// 启动后立刻退出通常意味着启动失败，给出可操作的方向。
	startFailed := exitCode != 0 && !stopWanted && duration < 5*time.Second
	if startFailed {
		r.appendLog("=== 提示：程序启动后立即退出，常见原因是端口被占用、" +
			"配置路径错误或缺少依赖库")
		r.appendLog("=== 可用 netstat -ano | findstr :端口号 检查端口占用")
	}

	if stateCallback != nil {
		stateCallback(r)
	}

	// 无论是否安排重启都通知一次，让上层决定是否发消息。
	if exitCallback != nil {
		exitCallback(r, ExitInfo{
			ExitCode:    exitCode,
			StartFailed: startFailed,
			Requested:   stopWanted,
			RunDuration: duration,
		})
	}

	if !wantRestart {
		return
	}

	r.appendLog("=== " + strconv.FormatFloat(float64(delayMs)/1000, 'f', -1, 64) +
		" 秒后自动重启")
	if crashCallback != nil {
		crashCallback(r, exitCode)
	}

	r.mu.Lock()
	r.restartGen++
	gen := r.restartGen
	r.mu.Unlock()
	time.AfterFunc(time.Duration(delayMs)*time.Millisecond, func() {
		r.mu.Lock()
		cancel := r.restartGen != gen || r.disposed || r.stopWanted
		r.mu.Unlock()
		if cancel {
			return
		}
		if err := r.Start(AppDir(), true); err != nil {
			r.appendLog("=== 自动重启失败：" + err.Error())
			r.setState(StateStopped)
		}
	})
}

// waitExit 阻塞到进程结束并返回退出码。
func (r *Runner) waitExit(handle *procHandle) int {
	if handle.cmd != nil {
		err := handle.cmd.Wait()
		if err == nil {
			return 0
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		return -1
	}

	// 提权进程：包装脚本在目标程序退出后才结束，因此先等它。
	result, err := windows.WaitForSingleObject(handle.sysHandle, windows.INFINITE)
	if err != nil || result != windows.WAIT_OBJECT_0 {
		windows.CloseHandle(handle.sysHandle)
		return -1
	}
	var code uint32
	if err := windows.GetExitCodeProcess(handle.sysHandle, &code); err != nil {
		windows.CloseHandle(handle.sysHandle)
		return -1
	}
	windows.CloseHandle(handle.sysHandle)

	return int(int32(code))
}

// drainElevationLog 把提权程序新增的输出转发到日志页。
// 轮询文件而不是等待管道，因为提权进程的管道无法直接交给父进程。
func (r *Runner) drainElevationLog(handle *procHandle) {
	if handle.logFile == "" {
		return
	}
	file, err := os.Open(handle.logFile)
	if err != nil {
		return
	}
	defer file.Close()
	if _, err := file.Seek(handle.logOffset, io.SeekStart); err != nil {
		return
	}
	data, err := io.ReadAll(file)
	if err != nil || len(data) == 0 {
		return
	}
	handle.logOffset += int64(len(data))
	for _, line := range strings.Split(string(data), "\n") {
		r.appendLog(strings.TrimRight(line, "\r"))
	}
}

// cleanupElevationFiles 删除一次提权启动产生的临时文件。
func cleanupElevationFiles(handle *procHandle) {
	if handle == nil {
		return
	}
	if handle.logFile != "" {
		os.Remove(handle.logFile)
	}
}

// Stop 请求停止：先执行优雅停止命令，超时后强制结束（含子进程）。
func (r *Runner) Stop() {
	r.mu.Lock()
	r.stopWanted = true
	r.restartGen++ // 取消待执行的重启
	handle := r.proc
	cfg := r.cfg
	r.mu.Unlock()

	if handle == nil {
		r.setState(StateStopped)
		return
	}

	r.setState(StateStopping)
	r.appendLog("=== 正在停止…")

	if stopArgs := cfg.buildStopCommand(); stopArgs != "" {
		if err := r.runDetached(cfg, stopArgs, 5000); err != nil {
			r.appendLog("=== 停止命令执行失败：" + err.Error())
		}
	}

	timeout := time.Duration(cfg.StopTimeout) * time.Millisecond
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	select {
	case <-handle.done:
	case <-time.After(timeout):
		r.appendLog("=== 等待超时，强制结束进程")
		killTree(handle.pid)
		select {
		case <-handle.done:
		case <-time.After(3 * time.Second):
		}
	}

	r.mu.Lock()
	if r.proc == handle {
		r.proc = nil
		r.startedAt = time.Time{}
	}
	r.state = StateStopped
	r.mu.Unlock()
	r.appendLog("=== 已停止")
	if cb := r.stateCallback(); cb != nil {
		cb(r)
	}
}

// Kill 立即强制结束，不等待正常退出（退出应用时使用）。
func (r *Runner) Kill() {
	r.mu.Lock()
	r.stopWanted = true
	r.restartGen++
	r.disposed = true
	handle := r.proc
	r.mu.Unlock()
	if handle != nil {
		killTree(handle.pid)
	}
}

// ---------------------------------------------------------------- 定时调度

// DueAction 描述本轮调度需要对某个程序执行的动作。
type DueAction int

const (
	ActionNone DueAction = iota
	ActionStart
	ActionStop
	ActionRestart
)

// CheckSchedule 判断当前时刻是否该启动/停止/周期重启，并记录触发状态。
// 由 UI 的秒级定时器调用，保证同一分钟内只触发一次。
func (r *Runner) CheckSchedule(now time.Time) DueAction {
	r.mu.Lock()
	defer r.mu.Unlock()
	cfg := r.cfg
	startSched, stopSched := r.schedulesLocked(cfg)

	if stopSched.Due(now, r.lastSchedStop) {
		r.lastSchedStop = now
		if r.proc != nil {
			return ActionStop
		}
	}

	if startSched.Due(now, r.lastSchedStart) {
		r.lastSchedStart = now
		if r.proc == nil {
			return ActionStart
		}
	}

	// 周期重启：从上一轮起点算起超过间隔就重启一次。
	if cfg.RestartEveryMin > 0 && r.proc != nil {
		interval := time.Duration(cfg.RestartEveryMin) * time.Minute
		if !r.lastRestartCycle.IsZero() && now.Sub(r.lastRestartCycle) >= interval {
			r.lastRestartCycle = now
			return ActionRestart
		}
	}
	return ActionNone
}

// schedulesLocked 按需解析并缓存定时规则。调用方需持有 r.mu。
// 非法表达式会被忽略，避免配置错误让调度器崩溃。
func (r *Runner) schedulesLocked(cfg *ProgramConfig) (start, stop *Schedule) {
	key := cfg.ScheduleStart + "\x00" + cfg.ScheduleStop
	if key == r.schedCacheKey {
		return r.startSched, r.stopSched
	}
	r.startSched = parseScheduleQuiet(cfg.ScheduleStart)
	r.stopSched = parseScheduleQuiet(cfg.ScheduleStop)
	r.schedCacheKey = key
	return r.startSched, r.stopSched
}

// parseScheduleQuiet 解析定时表达式，失败时返回 nil（不启用）。
func parseScheduleQuiet(text string) *Schedule {
	sched, err := ParseSchedule(text)
	if err != nil {
		return nil
	}
	return sched
}

// cronDue 判断当前分钟是否满足 cron 且本分钟尚未触发过。
func cronDue(now time.Time, spec *CronSpec, lastFired time.Time) bool {
	if !spec.Matches(now) {
		return false
	}
	return lastFired.Year() != now.Year() || lastFired.YearDay() != now.YearDay() ||
		lastFired.Hour() != now.Hour() || lastFired.Minute() != now.Minute()
}

// ScheduleSummary 返回界面展示用的调度说明。
func (r *Runner) ScheduleSummary() string {
	cfg := r.Config()
	var parts []string
	if sched := parseScheduleQuiet(cfg.ScheduleStart); sched != nil {
		parts = append(parts, "启动 "+sched.Display())
	}
	if sched := parseScheduleQuiet(cfg.ScheduleStop); sched != nil {
		parts = append(parts, "停止 "+sched.Display())
	}
	if cfg.RestartEveryMin > 0 {
		parts = append(parts, fmt.Sprintf("每 %d 分钟重启", cfg.RestartEveryMin))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "，")
}

// Probe 执行一次状态查询。未启用、未运行或正在探测时返回 nil。
func (r *Runner) Probe(baseDir string) *ProbeResult {
	r.mu.Lock()
	if !r.cfg.ProbeEnabled || r.probeBusy || r.proc == nil {
		r.mu.Unlock()
		return nil
	}
	cfg := r.cfg
	r.probeBusy = true
	r.mu.Unlock()

	defer func() {
		r.mu.Lock()
		r.probeBusy = false
		r.mu.Unlock()
	}()

	command := cfg.buildProbeCommand()
	if command == "" {
		return nil
	}
	output, err := r.runDetachedOutput(cfg, command, 5000)
	if err != nil {
		return nil
	}
	return parseProbe(cfg, output)
}

// ProbeDue 判断是否到了下一次探测时间，由 UI 定时器调用。
func (r *Runner) ProbeDue() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.cfg.ProbeEnabled || r.proc == nil {
		return false
	}
	return time.Since(r.lastProbeAt) >= time.Duration(r.cfg.ProbeInterval)*time.Millisecond
}

func (r *Runner) MarkProbed() {
	r.mu.Lock()
	r.lastProbeAt = time.Now()
	r.mu.Unlock()
}

func (r *Runner) runDetached(cfg *ProgramConfig, args string, timeoutMs int) error {
	_, err := r.runDetachedOutput(cfg, args, timeoutMs)
	return err
}

// runDetachedOutput 执行一次性命令并返回合并输出；超时杀进程并返回错误。
//
// 脚本程序（.py/.ps1/.js 等）不能直接执行脚本文件，探测与停止命令
// 必须像主进程一样通过解释器包装，否则必然失败。
func (r *Runner) runDetachedOutput(cfg *ProgramConfig, args string, timeoutMs int) (string, error) {
	launchExe, commandLine, err := cfg.WrapCommand(args)
	if err != nil {
		return "", err
	}
	if launchExe == "" {
		return "", errors.New("空命令")
	}
	ctx, cancel := context.WithTimeout(context.Background(),
		time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()

	cmd := exec.CommandContext(ctx, launchExe)
	cmd.Dir = cfg.resolveWorkDir(AppDir())
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow: true,
		// 与 startNormal 相同：CmdLine 必须以程序名开头。
		CmdLine: quoteArg(launchExe) + buildCmdLineSuffix(commandLine),
	}
	var buffer bytes.Buffer
	cmd.Stdout = &buffer
	cmd.Stderr = &buffer
	runErr := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return "", errors.New("查询超时")
	}
	return buffer.String(), runErr
}

// ---------------------------------------------------------------- 进程辅助

// processOutput 把子进程输出按行转成日志事件。
type processOutput struct{ runner *Runner }

func (w *processOutput) Write(p []byte) (int, error) {
	for _, line := range strings.Split(string(p), "\n") {
		w.runner.appendLog(strings.TrimRight(line, "\r"))
	}
	return len(p), nil
}

// hiddenProcAttr 让子进程不弹控制台窗口。
func hiddenProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW,
	}
}

// killTree 结束进程及其子进程。
func killTree(pid int) {
	cmd := exec.Command("taskkill.exe", "/PID", strconv.Itoa(pid), "/T", "/F")
	cmd.SysProcAttr = hiddenProcAttr()
	_ = cmd.Run()
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func filepathDir(path string) string {
	idx := strings.LastIndexAny(path, `\/`)
	if idx <= 0 {
		return "."
	}
	return path[:idx]
}

// ---------------------------------------------------------------- ShellExecuteEx

const (
	seeMaskNoCloseProcess = 0x00000040
	seeMaskNoUI           = 0x00000400
	seeMaskNoAsync        = 0x00000100
	swHide                = 0
	swShowMinNoActive     = 7
)

// shellExecuteInfo 对应 Windows 的 SHELLEXECUTEINFOW。
type shellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         uintptr
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     uintptr
	lpIDList     uintptr
	lpClass      *uint16
	hkeyClass    uintptr
	dwHotKey     uint32
	hIcon        uintptr
	hProcess     uintptr
}

// shellExecuteExRunAs 以管理员身份启动程序（触发 UAC），返回含进程句柄的结果。
// hideWindow 为 true 时使用 SW_HIDE，避免提权后的控制台窗口闪现。
func shellExecuteExRunAs(exePath, parameters, workDir string, hideWindow bool) (*shellExecuteInfo, error) {
	verb, err := windows.UTF16PtrFromString("runas")
	if err != nil {
		return nil, err
	}
	file, err := windows.UTF16PtrFromString(exePath)
	if err != nil {
		return nil, err
	}
	var params *uint16
	if parameters != "" {
		params, err = windows.UTF16PtrFromString(parameters)
		if err != nil {
			return nil, err
		}
	}
	var dir *uint16
	if workDir != "" {
		dir, err = windows.UTF16PtrFromString(workDir)
		if err != nil {
			return nil, err
		}
	}

	show := int32(swShowMinNoActive)
	if hideWindow {
		show = swHide
	}
	info := &shellExecuteInfo{
		fMask:        seeMaskNoCloseProcess | seeMaskNoUI | seeMaskNoAsync,
		lpVerb:       verb,
		lpFile:       file,
		lpParameters: params,
		lpDirectory:  dir,
		nShow:        show,
	}
	info.cbSize = uint32(unsafe.Sizeof(*info))

	shell32 := windows.NewLazySystemDLL("shell32.dll")
	proc := shell32.NewProc("ShellExecuteExW")
	ret, _, callErr := proc.Call(uintptr(unsafe.Pointer(info)))
	if ret == 0 {
		if info.hProcess != 0 {
			windows.CloseHandle(windows.Handle(info.hProcess))
		}
		// 用户取消 UAC 时返回 ERROR_CANCELLED(1223)，翻译成可读提示。
		if errno, ok := callErr.(syscall.Errno); ok && errno == 1223 {
			return nil, errors.New("用户取消了管理员授权")
		}
		return nil, callErr
	}
	if info.hProcess == 0 {
		return nil, errors.New("无法获取进程句柄")
	}
	return info, nil
}
