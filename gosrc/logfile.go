package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// 日志文件名形如 2026-11-20.log，日期由这个正则识别。
var logFilePattern = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})\.log$`)

// logWriter 按天把程序输出追加到文件。
//
// 每个被管理的程序一个子目录：
//
//	%APPDATA%\MultiPro\logs\<程序名>\2026-11-20.log
//
// 跨天时自动切换到新文件；写入失败只记录一次错误，不干扰程序运行。
// 所有方法都可从多个 goroutine 调用（标准输出与错误输出各一个）。
type logWriter struct {
	mu sync.Mutex

	programName string
	dir         string
	keepDays    int
	enabled     bool

	currentDate string
	file        *os.File
	// 写失败后置为 true，避免每行都重复尝试并刷日志。
	failed bool
}

// newLogWriter 创建写入器并确保目录存在。
func newLogWriter(programName string, keepDays int, enabled bool) *logWriter {
	w := &logWriter{
		programName: sanitizeName(programName),
		keepDays:    keepDays,
		enabled:     enabled,
	}
	if !enabled {
		return w
	}
	dir, err := logDir()
	if err != nil {
		w.failed = true
		return w
	}
	w.dir = filepath.Join(dir, w.programName)
	if err := os.MkdirAll(w.dir, 0o755); err != nil {
		w.failed = true
		return w
	}
	w.cleanup()
	return w
}

// logDir 返回日志根目录。
func logDir() (string, error) {
	// 日志放在程序所在目录，便于随程序一起备份或迁移。
	// 程序目录可能不可写（放在 Program Files 下），此时退回用户配置目录。
	primary := filepath.Join(AppDir(), "logs")
	if err := os.MkdirAll(primary, 0o755); err == nil {
		if probeWrite(primary) {
			migrateLegacyLogs(primary)
			return primary, nil
		}
	}
	base, err := configDir()
	if err != nil {
		return "", err
	}
	fallback := filepath.Join(base, "logs")
	if err := os.MkdirAll(fallback, 0o755); err != nil {
		return "", err
	}
	return fallback, nil
}

// migrateLegacyLogs 把旧版本写在配置目录下的日志搬到新位置。
//
// 旧版本使用 %APPDATA%\MultiPro\logs，升级后日志改到程序目录，
// 不迁移会让历史日志看起来「消失」。
//
// 配置文件在 C: 而程序可能在别的盘，os.Rename 跨卷会失败，
// 因此失败时退回「逐个文件复制 + 删除」。
func migrateLegacyLogs(target string) {
	base, err := configDir()
	if err != nil {
		return
	}
	legacy := filepath.Join(base, "logs")
	if legacy == target {
		return
	}
	if _, err := os.Stat(legacy); err != nil {
		return
	}

	// 逐层搬运：旧目录下每个程序一个子目录。
	programs, err := os.ReadDir(legacy)
	if err != nil {
		return
	}
	for _, program := range programs {
		srcDir := filepath.Join(legacy, program.Name())
		dstDir := filepath.Join(target, program.Name())

		if !program.IsDir() {
			// 根目录下的散落文件直接搬。
			moveFile(srcDir, filepath.Join(target, program.Name()))
			continue
		}
		if err := os.MkdirAll(dstDir, 0o755); err != nil {
			continue
		}
		files, err := os.ReadDir(srcDir)
		if err != nil {
			continue
		}
		for _, file := range files {
			if file.IsDir() {
				continue
			}
			moveFile(filepath.Join(srcDir, file.Name()), filepath.Join(dstDir, file.Name()))
		}
		// 源目录空了就删掉。
		if remaining, err := os.ReadDir(srcDir); err == nil && len(remaining) == 0 {
			_ = os.Remove(srcDir)
		}
	}
	// 旧根目录空了才删除，避免误删搬运失败的文件。
	if remaining, err := os.ReadDir(legacy); err == nil && len(remaining) == 0 {
		_ = os.Remove(legacy)
	}
}

// moveFile 优先用重命名（同卷时瞬时完成），跨卷失败时改为复制后删除。
// 目标已存在时不覆盖，避免丢掉新日志。
func moveFile(src, dst string) {
	if _, err := os.Stat(dst); err == nil {
		return
	}
	if err := os.Rename(src, dst); err == nil {
		return
	}
	// 跨卷：复制内容后删除源文件。
	data, err := os.ReadFile(src)
	if err != nil {
		return
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		// 复制失败时删掉半成品，保留源文件。
		_ = os.Remove(dst)
		return
	}
	_ = os.Remove(src)
}

// probeWrite 检查目录是否真的可写。
// 仅判断 MkdirAll 成功不够：目录可能存在但当前用户没有写权限。
func probeWrite(dir string) bool {
	probe := filepath.Join(dir, ".write-probe")
	file, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return false
	}
	file.Close()
	_ = os.Remove(probe)
	return true
}

// sanitizeName 把程序名转成合法目录名。
// Windows 文件名不能包含 \ / : * ? " < > |，统一替换成下划线。
func sanitizeName(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		trimmed = "未命名"
	}
	replacer := strings.NewReplacer(
		`\`, "_", `/`, "_", ":", "_", "*", "_",
		"?", "_", `"`, "_", "<", "_", ">", "_", "|", "_",
	)
	result := replacer.Replace(trimmed)
	// 去掉结尾的点和空格，Windows 不允许。
	result = strings.TrimRight(result, ". ")
	if result == "" {
		return "未命名"
	}
	return result
}

// Write 把一行输出写入当天的日志文件。
func (w *logWriter) Write(line string) {
	if w == nil || !w.enabled {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failed {
		return
	}

	now := time.Now()
	today := now.Format("2006-01-02")
	if w.file == nil || w.currentDate != today {
		if err := w.rotateLocked(today); err != nil {
			w.failed = true
			return
		}
	}

	// 每行都带时间戳，便于事后对照。
	timestamp := now.Format("2006-01-02 15:04:05")
	if _, err := fmt.Fprintf(w.file, "%s  %s\r\n", timestamp, line); err != nil {
		w.failed = true
	}
}

// rotateLocked 关闭旧文件并打开新一天的文件。调用方需持有锁。
func (w *logWriter) rotateLocked(date string) error {
	if w.file != nil {
		_ = w.file.Close()
		w.file = nil
	}
	path := filepath.Join(w.dir, date+".log")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	w.file = file
	w.currentDate = date
	// 跨天时顺便清理过期文件。
	w.cleanupLocked()
	return nil
}

// cleanup 删除超过保留天数的日志文件。
func (w *logWriter) cleanup() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.cleanupLocked()
}

// cleanupLocked 由清理逻辑复用。调用方需持有锁。
func (w *logWriter) cleanupLocked() {
	if w.keepDays <= 0 || w.dir == "" {
		return
	}
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -w.keepDays)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		match := logFilePattern.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		fileDate, err := time.ParseInLocation("2006-01-02", match[1], time.Local)
		if err != nil {
			continue
		}
		// 当天及最近 keepDays 天内的文件保留。
		if fileDate.Before(cutoff.Truncate(24 * time.Hour)) {
			_ = os.Remove(filepath.Join(w.dir, entry.Name()))
		}
	}
}

// Close 关闭当前文件。
func (w *logWriter) Close() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file != nil {
		_ = w.file.Close()
		w.file = nil
	}
}
