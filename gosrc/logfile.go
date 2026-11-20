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
	base, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "logs"), nil
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
