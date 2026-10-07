package logging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"

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
			err = errors.Join(err, sink.queue.Close(context.Background()))
		}
		for _, writer := range m.writers {
			err = errors.Join(err, writer.Close())
		}
		managerOwnership.Lock()
		managerOwnership.active = false
		managerOwnership.Unlock()
	}()
	for _, category := range logCategories {
		prefix := string(category)
		if category == events.LogRuntime {
			prefix = "elbot"
		}
		writer, openErr := open(prefix)
		if openErr != nil {
			return nil, openErr
		}
		m.writers = append(m.writers, writer)
		queue, queueErr := signal.NewQueue(signal.QueueOptions{Name: "logging." + string(category), Capacity: 256, WaitForCapacity: true})
		if queueErr != nil {
			return nil, queueErr
		}
		m.sinks[category] = &logSink{queue: queue, writer: writer}
		// Temporary legacy accessors share the same physical writers. Audit and
		// Elnis do not inherit the runtime level, including on the legacy path.
		legacyLevel := level
		if category != events.LogRuntime {
			legacyLevel = "debug"
		}
		logger := New(legacyLevel, writer)
		switch category {
		case events.LogRuntime:
			m.runtime = logger
		case events.LogAudit:
			m.audit = logger
		case events.LogElnis:
			m.elnis = logger
		}
	}
	m.connection, err = events.LogSubmitted.Connect(m.HandleRecord, signal.ConnectOptions{Shutdown: signal.CancelPending})
	if err != nil {
		return nil, err
	}
	return m, nil
}

// HandleRecord only admits an already frozen record. Filtering and formatting
// happen in its category's consumer. Business code calls events.EmitLog.
func (m *Manager) HandleRecord(ctx context.Context, record events.LogRecord) error {
	sink, ok := m.sinks[record.Category]
	if !ok {
		return fmt.Errorf("logging: unknown category %q", record.Category)
	}
	return sink.queue.Submit(context.WithoutCancel(ctx), signal.Task{Shutdown: signal.Drain, Run: func(ctx context.Context) error {
		if record.Category == events.LogRuntime && record.Level < m.level {
			return nil
		}
		data, err := formatRecord(ctx, record, m.level <= slog.LevelDebug)
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
}

func (m *Manager) BeginClose() {
	if m == nil {
		return
	}
	m.beginOnce.Do(func() {
		for _, category := range logCategories {
			m.sinks[category].queue.BeginClose()
		}
		m.connection.Disconnect()
	})
}
