package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ArgItem 参数表里的一项，Key 同时是命令行里的 {key} 占位符。
type ArgItem struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// ProgramConfig 描述一个被管理的程序。所有字段都由界面编辑，
// 运行时不认识任何具体程序，只按模板拼命令行。
type ProgramConfig struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	ExePath     string    `json:"exePath"`
	WorkDir     string    `json:"workDir"`
	Arguments   string    `json:"arguments"`
	Args        []ArgItem `json:"args"`
	RunAsAdmin  bool      `json:"runAsAdmin"`
	StopCommand string    `json:"stopCommand"`
	StopTimeout int       `json:"stopTimeoutMs"`

	AutoRestart  bool `json:"autoRestart"`
	RestartDelay int  `json:"restartDelayMs"`
	AutoStart    bool `json:"autoStart"`

	ProbeEnabled   bool   `json:"probeEnabled"`
	ProbeCommand   string `json:"probeCommand"`
	ProbeParser    string `json:"probeParser"`
	ProbeStatusKey string `json:"probeStatusKey"`
	ProbeOKKey     string `json:"probeOkKey"`
	ProbeOKValue   string `json:"probeOkValue"`
	ProbeInterval  int    `json:"probeIntervalMs"`

	// ScriptMode 决定如何执行目标文件：
	// auto（按扩展名判断）/ direct（直接运行）/ cmd / powershell / python / node。
	ScriptMode string `json:"scriptMode"`
	// Interpreter 可选，自定义解释器路径；留空则从 PATH 查找。
	Interpreter string `json:"interpreter"`

	// ScheduleStart / ScheduleStop 同时接受两种写法，自动识别：
	//
	//	"08:30"       每天 08:30
	//	"0 8 * * 1-5" cron 表达式
	//
	// 留空表示不启用。RestartEveryMin 为周期性重启间隔（分钟），0 表示关闭。
	ScheduleStart   string `json:"scheduleStart"`
	ScheduleStop    string `json:"scheduleStop"`
	RestartEveryMin int    `json:"restartEveryMin"`

	// 以下两个字段是旧版本遗留（简单时间与 cron 分开存）。
	// 仅用于读取旧配置时迁移，保存时不再写入。
	CronStart string `json:"cronStart,omitempty"`
	CronStop  string `json:"cronStop,omitempty"`

	// NotifyWebhook 覆盖全局 Webhook；留空则用全局默认。
	NotifyWebhook string `json:"notifyWebhook"`
	// NotifyPlatform 覆盖全局平台（wecom / ding）；留空则跟随全局。
	NotifyPlatform string `json:"notifyPlatform"`

	// HangMode 控制该程序的卡死检测：
	//
	//	""（空）跟随全局；"on" 强制启用；"off" 强制禁用
	//
	// 完全不输出日志的程序（纯 GUI 常驻、按事件触发的守护进程）需要
	// 显式关掉，否则会被误判为无响应并反复重启。
	HangMode string `json:"hangMode"`
	// HangTimeoutMin 覆盖全局阈值；0 表示跟随全局。
	HangTimeoutMin int `json:"hangTimeoutMin"`
}

// 脚本执行方式。
const (
	ModeAuto       = "auto"
	ModeDirect     = "direct"
	ModeCmd        = "cmd"
	ModePowershell = "powershell"
	ModePython     = "python"
	ModeNode       = "node"
)

// scriptKinds 用于界面展示：扩展名 → 中文说明。
var scriptKinds = map[string]string{
	".bat":  "批处理",
	".cmd":  "批处理",
	".ps1":  "PowerShell",
	".py":   "Python",
	".js":   "Node.js",
	".vbs":  "VBScript",
	".sh":   "Shell",
	".exe":  "可执行文件",
}

// AppConfig 整体配置：程序列表加全局选项。
type AppConfig struct {
	Programs         []*ProgramConfig `json:"programs"`
	AutoStartManager bool             `json:"autoStartManager"`
	MinimizeToTray   bool             `json:"minimizeToTray"`
	ShowTrayHint     bool             `json:"showTrayHint"`

	// 崩溃通知。程序异常退出并触发自动重启时提醒用户。
	CrashNotify bool `json:"crashNotify"`
	// CrashSound 为 true 时播放系统提示音。
	CrashSound bool `json:"crashSound"`
	// CrashBalloon 为 true 时弹托盘气泡；窗口在前台时也弹，
	// 避免用户盯着界面却不知道后台程序挂了。
	CrashBalloon bool `json:"crashBalloon"`
	// CrashNotifyCooldownSec 同一程序的提醒最小间隔，防止疯狂重启时刷屏。
	CrashNotifyCooldownSec int `json:"crashNotifyCooldownSec"`
	// CrashNotifyInitialized 标记崩溃提醒相关字段是否已初始化过。
	//
	// JSON 里缺失的 bool 会解析成 false，无法区分「用户主动关闭」和
	// 「旧版本没这个字段」。旧版本写下的配置没有本字段（false），
	// 此时填入默认值；之后用户如何设置都会被保留。
	CrashNotifyInitialized bool `json:"crashNotifyInitialized"`

	// 日志文件。按天分割写入 %APPDATA%\MultiPro\logs\<程序名>\YYYY-MM-DD.log
	LogToFile bool `json:"logToFile"`
	// LogKeepDays 保留最近多少天的日志，超出自动删除。0 表示不清理。
	LogKeepDays int `json:"logKeepDays"`
	// LogFileInitialized 与 CrashNotifyInitialized 同理，用于区分
	// 「用户主动关闭」和「旧配置没有这个字段」。
	LogFileInitialized bool `json:"logFileInitialized"`

	// Notify 是机器人通知配置（企业微信 / 钉钉）。
	Notify NotifyConfig `json:"notify"`

	// HangDetect 卡死检测。进程存活但长时间没有日志输出即判定无响应。
	HangDetect      bool `json:"hangDetect"`
	HangTimeoutMin  int  `json:"hangTimeoutMin"`
	HangAutoRestart bool `json:"hangAutoRestart"`
	// HangInitialized 用于区分「用户关闭」与「旧配置缺失」。
	HangInitialized bool `json:"hangInitialized"`
}

// applyDefaults 为缺失的字段补上默认值。
// 只处理「没有历史值」的情况，不覆盖用户已有的选择。
func (c *AppConfig) applyDefaults() {
	if !c.CrashNotifyInitialized {
		c.CrashNotify = true
		c.CrashSound = true
		c.CrashBalloon = true
		c.CrashNotifyInitialized = true
	}
	if c.CrashNotifyCooldownSec <= 0 {
		c.CrashNotifyCooldownSec = 60
	}
	if !c.LogFileInitialized {
		c.LogToFile = true
		// 首次启用时给一个合理的保留期。
		// 注意：0 在配置里有明确含义（永久保留），所以默认值只能
		// 放在这个分支里；若放在外面判断 0 就会被用户的选择覆盖掉。
		if c.LogKeepDays <= 0 {
			c.LogKeepDays = 30
		}
		c.LogFileInitialized = true
	}
	if c.LogKeepDays < 0 {
		c.LogKeepDays = 30
	}

	if !c.HangInitialized {
		c.HangDetect = true
		c.HangAutoRestart = true
		c.HangInitialized = true
	}
	if c.HangTimeoutMin <= 0 {
		c.HangTimeoutMin = 10
	}

	// 通知配置来自旧版本时补齐默认值。
	if c.Notify.Platform == "" {
		c.Notify.Platform = RobotWeCom
	}
	if c.Notify.LogTailLines <= 0 {
		c.Notify.LogTailLines = 10
	}
}

func newProgramConfig() *ProgramConfig {
	return &ProgramConfig{
		ID:            newID(),
		Name:          "新程序",
		StopTimeout:   5000,
		RestartDelay:  5000,
		ProbeParser:   "Lines",
		ProbeInterval: 3000,
		ScriptMode:    ModeAuto,
	}
}

func defaultAppConfig() *AppConfig {
	return &AppConfig{
		Programs:       []*ProgramConfig{},
		MinimizeToTray: true,
		ShowTrayHint:   true,

		CrashNotify:            true,
		CrashSound:             true,
		CrashBalloon:           true,
		CrashNotifyCooldownSec: 60,
		CrashNotifyInitialized: true,

		LogToFile:          true,
		LogKeepDays:        30,
		LogFileInitialized: true,

		Notify: defaultNotifyConfig(),

		HangDetect:      true,
		HangTimeoutMin:  10,
		HangAutoRestart: true,
		HangInitialized: true,
	}
}

// crashCooldown 返回崩溃提醒的最小间隔。
func (c *AppConfig) crashCooldown() time.Duration {
	seconds := c.CrashNotifyCooldownSec
	if seconds <= 0 {
		seconds = 60
	}
	return time.Duration(seconds) * time.Second
}

// ---------------------------------------------------------------- 配置目录

func configDir() (string, error) {
	base, err := os.UserConfigDir() // %APPDATA%
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "MultiPro")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

func configPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// loadConfig 读取配置。主文件损坏时尝试备份，仍失败则退回默认配置，
// 不阻塞启动。
func loadConfig() *AppConfig {
	path, err := configPath()
	if err != nil {
		return defaultAppConfig()
	}
	cfg, ok := readConfigFile(path)
	if !ok {
		// 主文件缺失或损坏：若存在备份就从备份恢复。
		if backup, backupOK := readConfigFile(path + ".bak"); backupOK {
			crashLog("配置读取失败，已从备份恢复：%s", path)
			_ = backup.save()
			return backup
		}
		return defaultAppConfig()
	}
	return cfg
}

// readConfigFile 读取并解析一个配置文件，返回是否成功。
func readConfigFile(path string) (*AppConfig, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var cfg AppConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, false
	}
	if cfg.Programs == nil {
		cfg.Programs = []*ProgramConfig{}
	}
	for _, p := range cfg.Programs {
		normalizeProgram(p)
	}
	cfg.applyDefaults()
	return &cfg, true
}

func normalizeProgram(p *ProgramConfig) {
	if p.ID == "" {
		p.ID = newID()
	}
	if p.Args == nil {
		p.Args = []ArgItem{}
	}
	if p.ProbeParser == "" {
		p.ProbeParser = "Lines"
	}
	if p.ProbeInterval <= 0 {
		p.ProbeInterval = 3000
	}
	if p.RestartDelay <= 0 {
		p.RestartDelay = 5000
	}
	if p.StopTimeout <= 0 {
		p.StopTimeout = 5000
	}
	if p.ScriptMode == "" {
		p.ScriptMode = ModeAuto
	}
	// 卡死检测模式只接受三个值，其他一律视为「跟随全局」。
	switch strings.ToLower(strings.TrimSpace(p.HangMode)) {
	case "on", "off":
		p.HangMode = strings.ToLower(strings.TrimSpace(p.HangMode))
	default:
		p.HangMode = ""
	}
	// 迁移旧配置：以前把 HH:MM 和 cron 分成两个字段，cron 优先。
	// 现在合并成一个字段，cron 若有值就覆盖简单时间，然后清空旧字段。
	if strings.TrimSpace(p.CronStart) != "" {
		p.ScheduleStart = p.CronStart
	}
	if strings.TrimSpace(p.CronStop) != "" {
		p.ScheduleStop = p.CronStop
	}
	p.CronStart = ""
	p.CronStop = ""
}

// save 把配置写回磁盘。
//
// 直接覆盖写有两个风险：写到一半崩溃会留下半个文件，导致下次启动
// 读不出配置（所有任务丢失）；磁盘满或断电也有同样问题。
// 因此先写同目录的临时文件并刷盘，再原子替换正式文件。
func (c *AppConfig) save() error {
	path, err := configPath()
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}

	tmp := path + ".tmp"
	file, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := file.Write(raw); err != nil {
		file.Close()
		os.Remove(tmp)
		return err
	}
	// 确保内容真正落盘后再替换，避免断电留下空文件。
	if err := file.Sync(); err != nil {
		file.Close()
		os.Remove(tmp)
		return err
	}
	if err := file.Close(); err != nil {
		os.Remove(tmp)
		return err
	}

	// 保留上一份配置作为备份，手工改坏时还能找回。
	// 只在正式文件存在时备份，失败不影响保存本身。
	if _, statErr := os.Stat(path); statErr == nil {
		_ = copyFile(path, path+".bak")
	}

	return replaceFile(tmp, path)
}

// copyFile 复制文件内容，用于保留配置备份。
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

// ---------------------------------------------------------------- 程序配置

func (p *ProgramConfig) clone() *ProgramConfig {
	copy := *p
	copy.Args = make([]ArgItem, len(p.Args))
	for i, a := range p.Args {
		copy.Args[i] = a
	}
	return &copy
}

// expand 替换 {key} 占位符，再展开 %ENV% 环境变量。
// 未定义的占位符原样保留，便于用户发现拼写错误。
func (p *ProgramConfig) expand(template string) string {
	result := template
	for _, a := range p.Args {
		if a.Key == "" {
			continue
		}
		result = strings.ReplaceAll(result, "{"+a.Key+"}", a.Value)
	}
	return os.ExpandEnv(result)
}

func (p *ProgramConfig) buildCommandLine() string {
	return strings.TrimSpace(p.expand(p.Arguments))
}

func (p *ProgramConfig) buildStopCommand() string {
	return strings.TrimSpace(p.expand(p.StopCommand))
}

func (p *ProgramConfig) buildProbeCommand() string {
	return strings.TrimSpace(p.expand(p.ProbeCommand))
}

// resolveExePath 解析程序路径：根路径 → 相对本程序目录 → PATH 查找。
func (p *ProgramConfig) resolveExePath(baseDir string) string {
	raw := strings.TrimSpace(os.ExpandEnv(p.ExePath))
	if raw == "" {
		return ""
	}
	if filepath.IsAbs(raw) {
		if abs, err := filepath.Abs(raw); err == nil {
			return abs
		}
		return raw
	}
	local := filepath.Join(baseDir, raw)
	if isFile(local) {
		if abs, err := filepath.Abs(local); err == nil {
			return abs
		}
		return local
	}
	if found, err := exec.LookPath(raw); err == nil {
		if abs, err := filepath.Abs(found); err == nil {
			return abs
		}
		return found
	}
	return local
}

func (p *ProgramConfig) resolveWorkDir(baseDir string) string {
	raw := strings.TrimSpace(os.ExpandEnv(p.WorkDir))
	if raw == "" {
		return baseDir
	}
	if !filepath.IsAbs(raw) {
		raw = filepath.Join(baseDir, raw)
	}
	if abs, err := filepath.Abs(raw); err == nil {
		return abs
	}
	return raw
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// ---------------------------------------------------------------- 脚本执行

// EffectiveMode 解析最终的执行方式。auto 模式下按扩展名推断，
// 无法识别时按直接运行处理。
func (p *ProgramConfig) EffectiveMode() string {
	mode := strings.ToLower(strings.TrimSpace(p.ScriptMode))
	if mode == "" || mode == ModeAuto {
		return modeForExtension(filepath.Ext(p.ExePath))
	}
	return mode
}

func modeForExtension(ext string) string {
	switch strings.ToLower(ext) {
	case ".bat", ".cmd":
		return ModeCmd
	case ".ps1":
		return ModePowershell
	case ".py":
		return ModePython
	case ".js":
		return ModeNode
	case ".vbs":
		return "wscript"
	case ".sh":
		return "sh"
	default:
		return ModeDirect
	}
}

// ScriptLabel 返回界面展示用的类型说明，例如「批处理」。
func (p *ProgramConfig) ScriptLabel() string {
	mode := p.EffectiveMode()
	switch mode {
	case ModeCmd:
		return "批处理"
	case ModePowershell:
		return "PowerShell"
	case ModePython:
		return "Python"
	case ModeNode:
		return "Node.js"
	case "wscript":
		return "VBScript"
	case "sh":
		return "Shell"
	default:
		if ext := strings.ToLower(filepath.Ext(p.ExePath)); scriptKinds[ext] != "" {
			return scriptKinds[ext]
		}
		return "可执行文件"
	}
}

// interpreterCandidates 返回该执行方式下可尝试的解释器（含默认参数）。
type interpreterSpec struct {
	names []string
	args  []string
}

func (p *ProgramConfig) interpreter() *interpreterSpec {
	switch p.EffectiveMode() {
	case ModeCmd:
		return &interpreterSpec{names: []string{"cmd.exe"}, args: []string{"/c"}}
	case ModePowershell:
		return &interpreterSpec{
			names: []string{"powershell.exe", "pwsh.exe"},
			// -File 后紧跟脚本路径，由调用方拼接。
			args: []string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-File"},
		}
	case ModePython:
		return &interpreterSpec{
			names: []string{"python.exe", "py.exe", "python3.exe"},
			args:  []string{},
		}
	case ModeNode:
		return &interpreterSpec{names: []string{"node.exe"}, args: []string{}}
	case "wscript":
		return &interpreterSpec{names: []string{"wscript.exe"}, args: []string{}}
	case "sh":
		return &interpreterSpec{names: []string{"bash.exe", "sh.exe"}, args: []string{}}
	default:
		return nil
	}
}

// resolveInterpreter 找到可用的解释器路径。自定义路径优先。
func (p *ProgramConfig) resolveInterpreter() (string, []string, error) {
	spec := p.interpreter()
	if spec == nil {
		return "", nil, nil
	}
	custom := strings.TrimSpace(os.ExpandEnv(p.Interpreter))
	if custom != "" {
		if path, err := exec.LookPath(custom); err == nil {
			return path, spec.args, nil
		}
		if isFile(custom) {
			return custom, spec.args, nil
		}
		return "", nil, fmt.Errorf("找不到指定的解释器：%s", custom)
	}
	for _, name := range spec.names {
		if path, err := exec.LookPath(name); err == nil {
			return path, spec.args, nil
		}
	}
	return "", nil, fmt.Errorf("找不到 %s 解释器，请先安装或手动指定路径", p.ScriptLabel())
}

// NeedsInterpreter 判断是否需要用解释器包装运行。
func (p *ProgramConfig) NeedsInterpreter() bool {
	return p.EffectiveMode() != ModeDirect
}

// BuildLaunch 返回最终要执行的 exe 与完整命令行。
// 脚本模式下会插入解释器；命令行的占位符替换同样生效。
func (p *ProgramConfig) BuildLaunch(baseDir string) (string, string, error) {
	exePath := p.resolveExePath(baseDir)
	if exePath == "" || !isFile(exePath) {
		return "", "", fmt.Errorf("找不到程序文件：%s", exePath)
	}
	commandLine := p.buildCommandLine()

	if !p.NeedsInterpreter() {
		return exePath, commandLine, nil
	}

	interpreterPath, interpreterArgs, err := p.resolveInterpreter()
	if err != nil {
		return "", "", err
	}
	parts := append([]string{}, interpreterArgs...)
	parts = append(parts, quoteForCommandLine(exePath))
	if commandLine != "" {
		parts = append(parts, commandLine)
	}
	return interpreterPath, strings.Join(parts, " "), nil
}

// quoteForCommandLine 对含空格/特殊字符的参数加引号。
func quoteForCommandLine(s string) string {
	if s == "" {
		return `""`
	}
	if !strings.ContainsAny(s, " \t&()[]{}^=;!'+,`~") {
		return s
	}
	return `"` + s + `"`
}

// ---------------------------------------------------------------- 探测结果

// StatusField 探测输出里的一行键值。
type StatusField struct {
	Key   string
	Value string
}

// ProbeLevel 探测得到的状态等级。
type ProbeLevel int

const (
	ProbeUnknown ProbeLevel = iota
	ProbeOK
	ProbeBad
)

// ProbeResult 一次探测的完整结果。
type ProbeResult struct {
	Fields []StatusField
	Level  ProbeLevel
	Raw    string
}

// parseProbe 把输出解析成字段和状态。三种模式覆盖绝大多数 CLI。
func parseProbe(cfg *ProgramConfig, output string) *ProbeResult {
	result := &ProbeResult{Raw: output}
	if strings.TrimSpace(output) == "" {
		return result
	}
	switch strings.ToLower(cfg.ProbeParser) {
	case "keyvalue":
		parseKeyValue(cfg, output, result)
	case "json":
		parseJSONOutput(cfg, output, result)
	default:
		parseLines(output, result)
	}
	return result
}

func parseLines(output string, result *ProbeResult) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		result.Fields = append(result.Fields, StatusField{Value: line})
	}
	if len(result.Fields) > 0 {
		result.Level = ProbeOK
	} else {
		result.Level = ProbeBad
	}
}

func parseKeyValue(cfg *ProgramConfig, output string, result *ProbeResult) {
	values := map[string]string{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		idx := strings.Index(line, ":")
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		value := strings.TrimSpace(line[idx+1:])
		values[strings.ToLower(key)] = value
		result.Fields = append(result.Fields, StatusField{Key: key, Value: value})
	}
	if len(result.Fields) == 0 {
		result.Level = ProbeBad
		return
	}
	result.Level = evaluateLevel(cfg, values)
}

func parseJSONOutput(cfg *ProgramConfig, output string, result *ProbeResult) {
	var data map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &data); err != nil {
		// JSON 不完整时退回行模式，至少让用户看到原始输出。
		parseLines(output, result)
		return
	}
	values := map[string]string{}
	flattenJSON("", data, values)
	for key, value := range values {
		result.Fields = append(result.Fields, StatusField{Key: key, Value: value})
	}
	if len(result.Fields) == 0 {
		result.Level = ProbeBad
		return
	}
	result.Level = evaluateLevel(cfg, values)
}

func flattenJSON(prefix string, data map[string]any, out map[string]string) {
	for key, value := range data {
		full := key
		if prefix != "" {
			full = prefix + "." + key
		}
		switch typed := value.(type) {
		case map[string]any:
			flattenJSON(full, typed, out)
		case []any:
			parts := make([]string, 0, len(typed))
			for _, item := range typed {
				parts = append(parts, fmt.Sprint(item))
			}
			out[full] = strings.Join(parts, ",")
		case nil:
			out[full] = ""
		default:
			out[full] = fmt.Sprint(typed)
		}
	}
}

// evaluateLevel 按「正常判断键/值」判定状态。没配键时只要有输出就算正常。
func evaluateLevel(cfg *ProgramConfig, values map[string]string) ProbeLevel {
	key := strings.TrimSpace(cfg.ProbeOKKey)
	if key == "" {
		return ProbeOK
	}
	value, found := values[strings.ToLower(key)]
	if !found {
		return ProbeUnknown
	}
	expected := strings.TrimSpace(cfg.ProbeOKValue)
	if expected == "" {
		if value != "" {
			return ProbeOK
		}
		return ProbeBad
	}
	if strings.EqualFold(strings.TrimSpace(value), expected) {
		return ProbeOK
	}
	return ProbeBad
}

func newID() string {
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), os.Getpid())
}

// ---------------------------------------------------------------- 定时调度

// parseClock 解析 HH:MM 格式的时间。空串或非法值返回错误。
func parseClock(value string) (hour, minute int, err error) {
	text := strings.TrimSpace(value)
	if text == "" {
		return 0, 0, fmt.Errorf("时间为空")
	}
	parts := strings.Split(text, ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("时间格式应为 HH:MM")
	}
	hour, errH := strconv.Atoi(strings.TrimSpace(parts[0]))
	minute, errM := strconv.Atoi(strings.TrimSpace(parts[1]))
	if errH != nil || errM != nil {
		return 0, 0, fmt.Errorf("时间格式应为 HH:MM")
	}
	if hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, 0, fmt.Errorf("时间超出范围")
	}
	return hour, minute, nil
}

// normalizeClock 把 9:5 这类输入规范成 09:05。
func normalizeClock(value string) string {
	hour, minute, err := parseClock(value)
	if err != nil {
		return strings.TrimSpace(value)
	}
	return fmt.Sprintf("%02d:%02d", hour, minute)
}

// scheduleDue 判断当前分钟是否正好是该定时点的触发时刻。
// lastFired 记录上次触发的日期+时间，避免同一分钟内重复触发。
func scheduleDue(now time.Time, clock string, lastFired time.Time) bool {
	hour, minute, err := parseClock(clock)
	if err != nil {
		return false
	}
	if now.Hour() != hour || now.Minute() != minute {
		return false
	}
	return lastFired.Hour() != hour || lastFired.Minute() != minute ||
		lastFired.YearDay() != now.YearDay() || lastFired.Year() != now.Year()
}

// isClockExpression 判断输入是「每天 HH:MM」还是 cron 表达式。
// 约定：含空格就是 cron（cron 必须 5 段），否则按 HH:MM 处理。
func isClockExpression(text string) bool {
	return !strings.Contains(strings.TrimSpace(text), " ")
}

// ParseSchedule 解析用户填写的定时表达式，返回统一的调度规则。
// 支持两种写法，自动识别：
//
//	"08:30"      → 每天 08:30
//	"0 8 * * 1-5" → cron
//
// 空字符串返回 (nil, nil) 表示不启用。
func ParseSchedule(text string) (*Schedule, error) {
	value := strings.TrimSpace(text)
	if value == "" {
		return nil, nil
	}
	if isClockExpression(value) {
		hour, minute, err := parseClock(value)
		if err != nil {
			return nil, fmt.Errorf("时间格式应为 HH:MM 或 5 段 cron：%v", err)
		}
		return &Schedule{Clock: fmt.Sprintf("%02d:%02d", hour, minute)}, nil
	}
	spec, err := ParseCron(value)
	if err != nil {
		return nil, fmt.Errorf("cron 表达式无效：%v", err)
	}
	return &Schedule{Cron: spec, Raw: value}, nil
}

// Schedule 表示一条已解析的定时规则，要么是每天固定时刻，要么是 cron。
type Schedule struct {
	// Clock 非空时表示每天的这个时刻。
	Clock string
	// Cron 非空时按表达式匹配。
	Cron *CronSpec
	// Raw 是用户填写的原始文本，用于界面展示。
	Raw string
}

// Due 判断当前时刻是否应触发，同一分钟内只触发一次。
func (s *Schedule) Due(now time.Time, lastFired time.Time) bool {
	if s == nil {
		return false
	}
	if s.Cron != nil {
		return cronDue(now, s.Cron, lastFired)
	}
	return scheduleDue(now, s.Clock, lastFired)
}

// Describe 返回中文说明。
func (s *Schedule) Describe() string {
	if s == nil {
		return ""
	}
	if s.Cron != nil {
		return s.Cron.Describe()
	}
	return "每天 " + s.Clock
}

// Display 返回列表里显示的简短文本。
func (s *Schedule) Display() string {
	if s == nil {
		return ""
	}
	if s.Cron != nil {
		return s.Raw
	}
	return s.Clock
}
