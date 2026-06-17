package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDailyFileWriterRotatesWhenDateChanges(t *testing.T) {
	dir := t.TempDir()
	current := time.Date(2026, 6, 16, 23, 59, 0, 0, time.Local)
	writer, err := newDailyFileWriter(filepath.Join(dir, "logs"), "elbot", func() time.Time { return current })
	if err != nil {
		t.Fatalf("newDailyFileWriter: %v", err)
	}
	logger := New("info", writer)

	logger.Info("before midnight")
	current = time.Date(2026, 6, 17, 0, 1, 0, 0, time.Local)
	logger.Info("after midnight")
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	oldData, err := os.ReadFile(filepath.Join(dir, "logs", "elbot-2026-06-16.log"))
	if err != nil {
		t.Fatalf("read old log: %v", err)
	}
	newData, err := os.ReadFile(filepath.Join(dir, "logs", "elbot-2026-06-17.log"))
	if err != nil {
		t.Fatalf("read new log: %v", err)
	}
	if !strings.Contains(string(oldData), "before midnight") || strings.Contains(string(oldData), "after midnight") {
		t.Fatalf("old log content = %q", oldData)
	}
	if !strings.Contains(string(newData), "after midnight") || strings.Contains(string(newData), "before midnight") {
		t.Fatalf("new log content = %q", newData)
	}
}

func TestDailyFileWriterClosePreventsFurtherWrites(t *testing.T) {
	writer, err := newDailyFileWriter(filepath.Join(t.TempDir(), "logs"), "elbot", nil)
	if err != nil {
		t.Fatalf("newDailyFileWriter: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	if _, err := writer.Write([]byte("after close")); err == nil {
		t.Fatal("Write after Close should fail")
	}
}

func TestManagerCreatesRuntimeAndAuditLogs(t *testing.T) {
	dir := t.TempDir()
	manager, err := NewManager("info", filepath.Join(dir, "elbot_sessions.db"), 30)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	manager.Runtime().Info("runtime log")
	manager.Audit().Info("audit log")
	if err := manager.Close(); err != nil {
		t.Fatalf("close manager: %v", err)
	}

	date := time.Now().Format("2006-01-02")
	for _, name := range []string{"elbot-" + date + ".log", "audit-" + date + ".log"} {
		data, err := os.ReadFile(filepath.Join(dir, "logs", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if len(data) == 0 {
			t.Fatalf("%s is empty", name)
		}
	}
}

func TestCleanupOldLogsRemovesExpiredManagedLogs(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("mkdir log dir: %v", err)
	}
	oldLog := filepath.Join(logDir, "elbot-2000-01-01.log")
	if err := os.WriteFile(oldLog, []byte("old"), 0o644); err != nil {
		t.Fatalf("write old log: %v", err)
	}
	oldTime := time.Now().AddDate(0, 0, -DefaultRetentionDays-1)
	if err := os.Chtimes(oldLog, oldTime, oldTime); err != nil {
		t.Fatalf("chtimes old log: %v", err)
	}

	logger, file, err := NewFile("info", filepath.Join(dir, "elbot_sessions.db"))
	if err != nil {
		t.Fatalf("NewFile: %v", err)
	}
	logger.Info("hello log")
	if err := file.Close(); err != nil {
		t.Fatalf("close log file: %v", err)
	}
	if _, err := os.Stat(oldLog); err != nil {
		t.Fatalf("old log should remain before explicit cleanup: %v", err)
	}

	if err := cleanupOldLogs(logDir, DefaultRetentionDays); err != nil {
		t.Fatalf("cleanupOldLogs: %v", err)
	}

	if _, err := os.Stat(oldLog); !os.IsNotExist(err) {
		t.Fatalf("old log still exists or stat failed: %v", err)
	}
	todayLog := filepath.Join(logDir, "elbot-"+time.Now().Format("2006-01-02")+".log")
	data, err := os.ReadFile(todayLog)
	if err != nil {
		t.Fatalf("read today log: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("today log is empty")
	}
}
