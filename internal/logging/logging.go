package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"elbot/internal/events"
	"elbot/internal/signal"
)

const DefaultRetentionDays = 30

type Manager struct {
	logDir        string
	retentionDays int
	writers       []io.WriteCloser
	level         slog.Level
	sinks         map[events.LogCategory]*logSink
	connection    *signal.Connection
	beginOnce     sync.Once
	closeGate     chan struct{}
	closed        bool
}

func NewManager(level, sqlitePath string, retentionDays int) (*Manager, error) {
	logDir := filepath.Join(filepath.Dir(sqlitePath), "logs")
	return newManager(level, logDir, retentionDays, func(prefix string) (io.WriteCloser, error) {
		return newDailyFileWriter(logDir, prefix, time.Now)
	})
}

func (m *Manager) LogDir() string {
	if m == nil {
		return ""
	}
	return m.logDir
}

func (m *Manager) RetentionDays() int {
	if m == nil {
		return DefaultRetentionDays
	}
	return m.retentionDays
}

func (m *Manager) CleanupOldLogs() error {
	if m == nil {
		return nil
	}
	return cleanupOldLogs(m.logDir, m.retentionDays)
}

func (m *Manager) Close(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.BeginClose()
	select {
	case m.closeGate <- struct{}{}:
	default:
		select {
		case m.closeGate <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	defer func() { <-m.closeGate }()
	if m.closed {
		return nil
	}
	for _, category := range logCategories {
		if err := m.sinks[category].queue.Close(ctx); err != nil {
			return err
		}
	}
	for _, writer := range m.writers {
		if err := writer.Close(); err != nil {
			signal.ReportDiagnostic("logging writer close failed", slog.Any("error", err))
		}
	}
	m.closed = true
	managerOwnership.Lock()
	managerOwnership.active = false
	managerOwnership.Unlock()
	return nil
}

type dailyFileWriter struct {
	mu     sync.Mutex
	logDir string
	prefix string
	now    func() time.Time
	day    string
	file   *os.File
}

func newDailyFileWriter(logDir, prefix string, now func() time.Time) (*dailyFileWriter, error) {
	writer := &dailyFileWriter{logDir: logDir, prefix: prefix, now: now}
	if err := writer.rotateLocked(); err != nil {
		return nil, err
	}
	return writer, nil
}

func (w *dailyFileWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file == nil {
		return 0, fmt.Errorf("log file writer is closed")
	}
	if err := w.rotateLocked(); err != nil {
		return 0, err
	}
	return w.file.Write(p)
}

func (w *dailyFileWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

func (w *dailyFileWriter) rotateLocked() error {
	day := w.now().Format("2006-01-02")
	if w.file != nil && w.day == day {
		return nil
	}
	if err := os.MkdirAll(w.logDir, 0o755); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}
	path := filepath.Join(w.logDir, w.prefix+"-"+day+".log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	old := w.file
	w.file = file
	w.day = day
	if old != nil {
		return old.Close()
	}
	return nil
}

func cleanupOldLogs(logDir string, days int) error {
	entries, err := os.ReadDir(logDir)
	if err != nil {
		return fmt.Errorf("read log dir: %w", err)
	}
	cutoff := time.Now().AddDate(0, 0, -days)
	for _, entry := range entries {
		if entry.IsDir() || !isManagedLogFile(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat log file: %w", err)
		}
		if info.ModTime().Before(cutoff) {
			if err := os.Remove(filepath.Join(logDir, entry.Name())); err != nil {
				return fmt.Errorf("remove old log file: %w", err)
			}
		}
	}
	return nil
}

func normalizeRetentionDays(days int) int {
	if days <= 0 {
		return DefaultRetentionDays
	}
	return days
}

func isManagedLogFile(name string) bool {
	if !strings.HasSuffix(name, ".log") {
		return false
	}
	return strings.HasPrefix(name, "elbot-") || strings.HasPrefix(name, "audit-") || strings.HasPrefix(name, "elnis-")
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
