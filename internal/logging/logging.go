package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const DefaultRetentionDays = 30

type Manager struct {
	runtime       *slog.Logger
	audit         *slog.Logger
	logDir        string
	retentionDays int
	files         []*os.File
}

func NewManager(level, sqlitePath string, retentionDays int) (*Manager, error) {
	logDir := filepath.Join(filepath.Dir(sqlitePath), "logs")
	retentionDays = normalizeRetentionDays(retentionDays)
	runtime, runtimeFile, err := newPrefixedFile(level, sqlitePath, "elbot")
	if err != nil {
		return nil, err
	}
	audit, auditFile, err := newPrefixedFile(level, sqlitePath, "audit")
	if err != nil {
		_ = runtimeFile.Close()
		return nil, err
	}
	return &Manager{runtime: runtime, audit: audit, logDir: logDir, retentionDays: retentionDays, files: []*os.File{runtimeFile, auditFile}}, nil
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
	for _, file := range m.files {
		if file == nil {
			continue
		}
		if err := file.Close(); err != nil && closeErr == nil {
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

func NewFile(level, sqlitePath string) (*slog.Logger, *os.File, error) {
	return newPrefixedFile(level, sqlitePath, "elbot")
}

func newPrefixedFile(level, sqlitePath, prefix string) (*slog.Logger, *os.File, error) {
	logDir := filepath.Join(filepath.Dir(sqlitePath), "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, nil, fmt.Errorf("create log dir: %w", err)
	}

	path := filepath.Join(logDir, prefix+"-"+time.Now().Format("2006-01-02")+".log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, nil, fmt.Errorf("open log file: %w", err)
	}
	return New(level, file), file, nil
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
