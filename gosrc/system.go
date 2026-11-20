package main

import (
	"errors"
	"os"
	"path/filepath"
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
		icon = fallbackIcon(cfg)
	}

	iconCacheMu.Lock()
	iconCache[key] = icon
	iconCacheMu.Unlock()
	return icon
}

// fallbackIcon 在没有可分提取图标时返回按类型区分的系统图标。
func fallbackIcon(cfg *ProgramConfig) walk.Image {
	index := 1 // 通用应用
	switch cfg.EffectiveMode() {
	case ModeCmd:
		index = 41 // 命令提示符
	case ModePowershell:
		index = 13
	case ModePython, ModeNode:
		index = 2
	}
	if icon, err := walk.NewIconFromSysDLLWithSize("imageres", index, 16); err == nil {
		return icon
	}
	return walk.IconApplication()
}

// clearIconCache 配置变化后清空缓存，让图标跟随新的 exe 路径。
func clearIconCache() {
	iconCacheMu.Lock()
	iconCache = map[string]walk.Image{}
	iconCacheMu.Unlock()
}
