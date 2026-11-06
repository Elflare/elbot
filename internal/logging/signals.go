package logging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"elbot/internal/events"
	"elbot/internal/signal"
)

var managerOwnership struct {
	sync.Mutex
	active bool
}
var logCategories = [...]events.LogCategory{events.LogRuntime, events.LogAudit, events.LogElnis}

type logSink struct {
	queue  *signal.Queue
	writer io.Writer
}

// The factory is internal so failure tests can exercise the real subscription,
// queue and formatting path without introducing a second logger implementation.
func newManager(level, logDir string, retentionDays int, open func(string) (io.WriteCloser, error)) (_ *Manager, err error) {
	managerOwnership.Lock()
	if managerOwnership.active {
		managerOwnership.Unlock()
		return nil, errors.New("logging: a manager is already active")
	}
	managerOwnership.active = true
	managerOwnership.Unlock()
	m := &Manager{logDir: logDir, retentionDays: normalizeRetentionDays(retentionDays), level: parseLevel(level), sinks: make(map[events.LogCategory]*logSink), closeGate: make(chan struct{}, 1)}
	defer func() {
		if err == nil {
			return
		}
		if m.connection != nil {
			m.connection.Disconnect()
		}
		for _, sink := range m.sinks {
			if closeErr := sink.queue.Close(context.Background()); closeErr != nil {
				signal.ReportDiagnostic("logging cleanup failed", slog.Any("error", closeErr))
			}
		}
		for _, writer := range m.writers {
			if closeErr := writer.Close(); closeErr != nil {
				signal.ReportDiagnostic("logging cleanup failed", slog.Any("error", closeErr))
			}
		}
		managerOwnership.Lock()
		managerOwnership.active = false
		managerOwnership.Unlock()
	}()
	for _, category := range logCategories {
		prefix := category.String()
		if category == events.LogRuntime {
			prefix = "elbot"
		}
		writer, openErr := open(prefix)
		if openErr != nil {
			return nil, openErr
		}
		m.writers = append(m.writers, writer)
		queue, queueErr := signal.NewQueue(signal.QueueOptions{Name: "logging." + category.String(), Capacity: 256, WaitForCapacity: true, AdmissionTimeout: 500 * time.Millisecond})
		if queueErr != nil {
			return nil, queueErr
		}
		m.sinks[category] = &logSink{queue: queue, writer: writer}
	}
	m.connection, err = events.LogSubmitted.Observe(m.handleRecord, signal.ConnectOptions{Shutdown: signal.Drain})
	if err != nil {
		return nil, err
	}
	return m, nil
}

// handleRecord only admits an already frozen record. Filtering and formatting
// happen in its category's consumer. Business code calls events.Log or events.AsyncLog.
func (m *Manager) handleRecord(ctx context.Context, record events.LogRecord) {
	at := time.Now()
	sink := m.sinks[record.Category]
	err := sink.queue.Submit(context.WithoutCancel(ctx), signal.Task{Shutdown: signal.Drain, Run: func(ctx context.Context) (failure error) {
		defer func() {
			if failure != nil {
				signal.ReportDiagnostic("logging record failed", slog.String("category", record.Category.String()), slog.Any("error", failure))
			}
		}()
		if record.Category == events.LogRuntime && record.Level < m.level {
			return nil
		}
		data, err := formatRecord(record, at)
		if err != nil {
			return err
		}
		n, err := sink.writer.Write(data)
		if err == nil && n != len(data) {
			err = io.ErrShortWrite
		}
		if err != nil {
			return fmt.Errorf("write %s log: %w", record.Category, err)
		}
		return nil
	}})
	if err != nil && !errors.Is(err, signal.ErrClosed) {
		signal.ReportDiagnostic("logging admission failed", slog.String("category", record.Category.String()), slog.Any("error", err))
	}
	return
}

func (m *Manager) BeginClose() {
	if m == nil {
		return
	}
	m.beginOnce.Do(func() {
		m.connection.BeginClose()
	})
}
