package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const DefaultRetentionDays = 30

type Manager struct {
	runtime       *slog.Logger
	audit         *slog.Logger
	elnis         *slog.Logger
	logDir        string
	retentionDays int
	writers       []*dailyFileWriter
}

func NewManager(level, sqlitePath string, retentionDays int) (*Manager, error) {
	logDir := filepath.Join(filepath.Dir(sqlitePath), "logs")
	retentionDays = normalizeRetentionDays(retentionDays)
	runtime, runtimeWriter, err := newPrefixedFile(level, sqlitePath, "elbot")
	if err != nil {
		return nil, err
	}
	audit, auditWriter, err := newPrefixedFile(level, sqlitePath, "audit")
	if err != nil {
		_ = runtimeWriter.Close()
		return nil, err
	}
	elnis, elnisWriter, err := newPrefixedFile(level, sqlitePath, "elnis")
	if err != nil {
		_ = runtimeWriter.Close()
		_ = auditWriter.Close()
		return nil, err
	}
	return &Manager{runtime: runtime, audit: audit, elnis: elnis, logDir: logDir, retentionDays: retentionDays, writers: []*dailyFileWriter{runtimeWriter, auditWriter, elnisWriter}}, nil
}

func (m *Manager) Runtime() *slog.Logger {
	if m == nil {
		return nil
	}
	return m.runtime
}

func (m *Manager) Audit() *slog.Logger {
	if m == nil {
		return nil
	}
	return m.audit
}

func (m *Manager) Elnis() *slog.Logger {
	if m == nil {
		return nil
	}
	return m.elnis
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

func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	var closeErr error
	for _, writer := range m.writers {
		if writer == nil {
			continue
		}
		if err := writer.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
	}
	return closeErr
}

func New(level string, output io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(output, &slog.HandlerOptions{
		Level:       parseLevel(level),
		ReplaceAttr: replaceAttr,
	}))
}

func replaceAttr(groups []string, attr slog.Attr) slog.Attr {
	if attr.Key == slog.TimeKey {
		if t, ok := attr.Value.Any().(time.Time); ok {
			attr.Value = slog.StringValue(t.Format("2006-01-02 15:04:05"))
		}
	}
	return attr
}

func NewFile(level, sqlitePath string) (*slog.Logger, io.Closer, error) {
	return newPrefixedFile(level, sqlitePath, "elbot")
}

func newPrefixedFile(level, sqlitePath, prefix string) (*slog.Logger, *dailyFileWriter, error) {
	logDir := filepath.Join(filepath.Dir(sqlitePath), "logs")
	writer, err := newDailyFileWriter(logDir, prefix, time.Now)
	if err != nil {
		return nil, nil, err
	}
	return New(level, writer), writer, nil
}

type dailyFileWriter struct {
	mu     sync.Mutex
	logDir string
	prefix string
	now    func() time.Time
	day    string
	file   *os.File
	closed bool
}

func newDailyFileWriter(logDir, prefix string, now func() time.Time) (*dailyFileWriter, error) {
	if strings.TrimSpace(logDir) == "" {
		return nil, fmt.Errorf("log directory is not configured")
	}
	if strings.TrimSpace(prefix) == "" {
		return nil, fmt.Errorf("log prefix is not configured")
	}
	if now == nil {
		now = time.Now
	}
	writer := &dailyFileWriter{logDir: logDir, prefix: prefix, now: now}
	if err := writer.rotateLocked(writer.currentDay()); err != nil {
		return nil, err
	}
	return writer, nil
}

func (w *dailyFileWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closed {
		return 0, fmt.Errorf("log file writer is closed")
	}
	if err := w.rotateLocked(w.currentDay()); err != nil {
		return 0, err
	}
	return w.file.Write(p)
}

func (w *dailyFileWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

func (w *dailyFileWriter) currentDay() string {
	return w.now().Format("2006-01-02")
}

func (w *dailyFileWriter) rotateLocked(day string) error {
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
	days = normalizeRetentionDays(days)
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
	return strings.HasPrefix(name, "elbot-") || strings.HasPrefix(name, "audit-")
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
