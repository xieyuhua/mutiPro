package main

import (
	"errors"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/lxn/walk"
)

const (
	runKeyPath   = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValueName = "MultiPro"
)

// AppDir 返回程序所在目录。被管理的程序按此目录解析相对路径。
func AppDir() string {
	exe, err := os.Executable()
	if err != nil {
		dir, _ := os.Getwd()
		return dir
	}
	return filepath.Dir(exe)
}

// isAdministrator 判断当前进程是否已提权。
func isAdministrator() bool {
	var sid *windows.SID
	err := windows.AllocateAndInitializeSid(
		&windows.SECURITY_NT_AUTHORITY,
		2,
		windows.SECURITY_BUILTIN_DOMAIN_RID,
		windows.DOMAIN_ALIAS_RID_ADMINS,
		0, 0, 0, 0, 0, 0,
		&sid,
	)
	if err != nil {
		return false
	}
	defer windows.FreeSid(sid)
	token := windows.Token(0)
	member, err := token.IsMember(sid)
	return err == nil && member
}

// relaunchAsAdministrator 以管理员身份重启本程序；成功后调用方应退出当前实例。
func relaunchAsAdministrator() error {
	exe := os.Args[0]
	if abs, err := filepath.Abs(exe); err == nil {
		exe = abs
	}
	full, err := os.Executable()
	if err == nil {
		exe = full
	}
	_, err = shellExecuteExRunAs(exe, "--elevated", AppDir(), false)
	return err
}

// applyAutoStart 写入或移除本程序的开机自启注册表项。
func applyAutoStart(enabled bool) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()

	if !enabled {
		err = key.DeleteValue(runValueName)
		if errors.Is(err, registry.ErrNotExist) || errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) {
			return nil
		}
		return err
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return key.SetStringValue(runValueName, `"`+exe+`" --minimized`)
}

// openConfigFolder 在资源管理器中打开配置目录。
func openConfigFolder() {
	dir, err := configDir()
	if err != nil {
		return
	}
	_ = execCommand("explorer.exe", dir)
}

// appIcon 返回程序图标。没有外部图标文件，使用系统内置的应用程序图标。
func appIcon() walk.Image {
	icon, err := walk.NewIconFromSysDLL("imageres", 11) // imageres.dll #11：齿轮/设置类图标
	if err == nil {
		return icon
	}
	return walk.IconApplication()
}

// trayIcon 返回托盘图标，同样使用系统内置图标，不依赖外部文件。
func trayIconImage() walk.Image {
	icon, err := walk.NewIconFromSysDLL("shell32", 14)
	if err == nil {
		return icon
	}
	return walk.IconApplication()
}

// ---------------------------------------------------------------- 程序图标

var (
	iconCacheMu sync.Mutex
	iconCache   = map[string]walk.Image{}
)

// programIcon 返回被管理程序在列表中显示的图标。
//
// 优先取目标 exe 自带的图标；脚本文件没有图标资源，改为显示对应解释器的
// 图标（批处理/PowerShell/Python/Node），这样列表里能一眼区分类型。
// 结果按路径缓存，读取失败时回退到系统图标，不影响功能。
func programIcon(cfg *ProgramConfig) walk.Image {
	key := cfg.ExePath + "|" + cfg.EffectiveMode()
	iconCacheMu.Lock()
	if cached, ok := iconCache[key]; ok {
		iconCacheMu.Unlock()
		return cached
	}
	iconCacheMu.Unlock()

	var icon walk.Image
	// 脚本模式显示解释器图标，其余显示目标文件图标。
	sourcePath := ""
	if cfg.NeedsInterpreter() {
		if interpreterPath, _, err := cfg.resolveInterpreter(); err == nil {
			sourcePath = interpreterPath
		}
	}
	if sourcePath == "" {
		path := cfg.resolveExePath(AppDir())
		if isFile(path) {
			sourcePath = path
		}
	}
	if sourcePath != "" {
		if extracted, err := walk.NewIconExtractedFromFileWithSize(sourcePath, 0, 16); err == nil {
			icon = extracted
		}
	}
	if icon == nil {
		// exe 本身没有图标资源（Go/C# 编译的很多程序都是这样），
		// 用程序名首字生成彩色图标，至少能在列表里区分开。
		icon = generatedIcon(cfg)
	}

	iconCacheMu.Lock()
	iconCache[key] = icon
	iconCacheMu.Unlock()
	return icon
}

// iconPalette 是生成图标使用的一组柔和背景色。
var iconPalette = []color.RGBA{
	{79, 70, 229, 255},  // 靛蓝
	{37, 99, 235, 255},  // 蓝
	{8, 145, 178, 255},  // 青
	{5, 150, 105, 255},  // 绿
	{202, 138, 4, 255},  // 琥珀
	{217, 70, 239, 255}, // 紫
	{220, 38, 38, 255},  // 红
	{71, 85, 105, 255},  // 石板灰
}

// generatedIcon 用程序名首字生成一个圆角方块图标。
// 颜色由名称哈希决定，因此同一个程序每次得到的颜色一致。
func generatedIcon(cfg *ProgramConfig) walk.Image {
	name := strings.TrimSpace(cfg.Name)
	if name == "" {
		name = filepath.Base(cfg.ExePath)
	}
	letter := firstRune(name)
	color := iconPalette[hashString(name)%uint32(len(iconPalette))]

	const size = 32 // 生成 32px，系统缩放时更清晰
	img := image.NewRGBA(image.Rect(0, 0, size, size))

	// 画圆角矩形背景
	radius := size / 5
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if inRoundedRect(x, y, size, radius) {
				img.SetRGBA(x, y, color)
			}
		}
	}

	// 居中写字。没有字体渲染库，用简单的 5x7 点阵覆盖常用字符，
	// 其余字符退化为一个方块，保证不会出现空白图标。
	drawGlyph(img, letter, size)

	icon, err := walk.NewIconFromImageForDPI(img, 96)
	if err == nil {
		return icon
	}
	return walk.IconApplication()
}

// inRoundedRect 判断像素是否落在圆角矩形内。
func inRoundedRect(x, y, size, radius int) bool {
	if x < 0 || y < 0 || x >= size || y >= size {
		return false
	}
	// 只有落在四角区域时才需要做圆判断。
	left, right := x < radius, x >= size-radius
	top, bottom := y < radius, y >= size-radius
	if !((left || right) && (top || bottom)) {
		return true
	}
	cx, cy := radius, radius
	if right {
		cx = size - radius - 1
	}
	if bottom {
		cy = size - radius - 1
	}
	dx, dy := x-cx, y-cy
	return dx*dx+dy*dy <= radius*radius
}

// glyph5x7 提供数字与常见字母的点阵，用于在生成图标里写字。
var glyph5x7 = map[rune][7]uint8{
	'A': {0x0E, 0x11, 0x11, 0x1F, 0x11, 0x11, 0x11},
	'B': {0x1E, 0x11, 0x11, 0x1E, 0x11, 0x11, 0x1E},
	'C': {0x0E, 0x11, 0x10, 0x10, 0x10, 0x11, 0x0E},
	'D': {0x1E, 0x11, 0x11, 0x11, 0x11, 0x11, 0x1E},
	'E': {0x1F, 0x10, 0x10, 0x1E, 0x10, 0x10, 0x1F},
	'F': {0x1F, 0x10, 0x10, 0x1E, 0x10, 0x10, 0x10},
	'G': {0x0E, 0x11, 0x10, 0x17, 0x11, 0x11, 0x0E},
	'H': {0x11, 0x11, 0x11, 0x1F, 0x11, 0x11, 0x11},
	'I': {0x0E, 0x04, 0x04, 0x04, 0x04, 0x04, 0x0E},
	'J': {0x07, 0x02, 0x02, 0x02, 0x02, 0x12, 0x0C},
	'K': {0x11, 0x12, 0x14, 0x18, 0x14, 0x12, 0x11},
	'L': {0x10, 0x10, 0x10, 0x10, 0x10, 0x10, 0x1F},
	'M': {0x11, 0x1B, 0x15, 0x15, 0x11, 0x11, 0x11},
	'N': {0x11, 0x19, 0x15, 0x13, 0x11, 0x11, 0x11},
	'O': {0x0E, 0x11, 0x11, 0x11, 0x11, 0x11, 0x0E},
	'P': {0x1E, 0x11, 0x11, 0x1E, 0x10, 0x10, 0x10},
	'Q': {0x0E, 0x11, 0x11, 0x11, 0x15, 0x12, 0x0D},
	'R': {0x1E, 0x11, 0x11, 0x1E, 0x14, 0x12, 0x11},
	'S': {0x0F, 0x10, 0x10, 0x0E, 0x01, 0x01, 0x1E},
	'T': {0x1F, 0x04, 0x04, 0x04, 0x04, 0x04, 0x04},
	'U': {0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x0E},
	'V': {0x11, 0x11, 0x11, 0x11, 0x11, 0x0A, 0x04},
	'W': {0x11, 0x11, 0x11, 0x15, 0x15, 0x1B, 0x11},
	'X': {0x11, 0x11, 0x0A, 0x04, 0x0A, 0x11, 0x11},
	'Y': {0x11, 0x11, 0x0A, 0x04, 0x04, 0x04, 0x04},
	'Z': {0x1F, 0x01, 0x02, 0x04, 0x08, 0x10, 0x1F},
	'0': {0x0E, 0x11, 0x13, 0x15, 0x19, 0x11, 0x0E},
	'1': {0x04, 0x0C, 0x04, 0x04, 0x04, 0x04, 0x0E},
	'2': {0x0E, 0x11, 0x01, 0x02, 0x04, 0x08, 0x1F},
	'3': {0x1F, 0x02, 0x04, 0x02, 0x01, 0x11, 0x0E},
	'4': {0x02, 0x06, 0x0A, 0x12, 0x1F, 0x02, 0x02},
	'5': {0x1F, 0x10, 0x1E, 0x01, 0x01, 0x11, 0x0E},
	'6': {0x06, 0x08, 0x10, 0x1E, 0x11, 0x11, 0x0E},
	'7': {0x1F, 0x01, 0x02, 0x04, 0x08, 0x08, 0x08},
	'8': {0x0E, 0x11, 0x11, 0x0E, 0x11, 0x11, 0x0E},
	'9': {0x0E, 0x11, 0x11, 0x0F, 0x01, 0x02, 0x0C},
}

// drawGlyph 把字符按 5x7 点阵画到图标中央。无法识别的字符画一个实心方块。
func drawGlyph(img *image.RGBA, letter rune, size int) {
	upper := letter
	if upper >= 'a' && upper <= 'z' {
		upper = upper - 'a' + 'A'
	}
	glyph, ok := glyph5x7[upper]
	if !ok {
		// 中文等宽字符：画一个居中的白色方块作为占位。
		boxSize := size / 2
		start := (size - boxSize) / 2
		for y := start; y < start+boxSize; y++ {
			for x := start; x < start+boxSize; x++ {
				img.SetRGBA(x, y, color.RGBA{255, 255, 255, 255})
			}
		}
		return
	}

	const gw, gh = 5, 7
	// 放大到约 55% 高度，保持整数倍缩放以免模糊。
	scale := size * 55 / 100 / gh
	if scale < 1 {
		scale = 1
	}
	offsetX := (size - gw*scale) / 2
	offsetY := (size - gh*scale) / 2
	for row := 0; row < gh; row++ {
		bits := glyph[row]
		for col := 0; col < gw; col++ {
			if bits&(1<<uint(4-col)) == 0 {
				continue
			}
			for dy := 0; dy < scale; dy++ {
				for dx := 0; dx < scale; dx++ {
					img.SetRGBA(offsetX+col*scale+dx, offsetY+row*scale+dy,
						color.RGBA{255, 255, 255, 255})
				}
			}
		}
	}
}

// firstRune 取字符串第一个字符（支持中文）。
func firstRune(text string) rune {
	for _, r := range text {
		return r
	}
	return '?'
}

// hashString 生成稳定哈希，用于给同一名称分配固定颜色。
func hashString(text string) uint32 {
	var h uint32 = 2166136261
	for _, r := range text {
		h ^= uint32(r)
		h *= 16777619
	}
	return h
}

// clearIconCache 配置变化后清空缓存，让图标跟随新的 exe 路径。
func clearIconCache() {
	iconCacheMu.Lock()
	iconCache = map[string]walk.Image{}
	iconCacheMu.Unlock()
}
