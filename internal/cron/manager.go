package cron

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"elbot/internal/storage"

	robfigcron "github.com/robfig/cron/v3"
)

type Handler func(ctx context.Context, job storage.CronJob) error

type Registry interface {
	RegisterHandler(name string, handler Handler) error
	UpsertJob(ctx context.Context, req UpsertJobRequest) (*storage.CronJob, error)
	DisableJob(ctx context.Context, name string) error
	DeleteJob(ctx context.Context, name string) error
}

type Manager struct {
	repo      storage.CronJobRepository
	logger    *slog.Logger
	scheduler *robfigcron.Cron

	mu       sync.Mutex
	handlers map[string]Handler
	entries  map[string]robfigcron.EntryID
	running  map[string]bool
	started  bool
}

type UpsertJobRequest = storage.UpsertCronJobRequest

func NewManager(repo storage.CronJobRepository, logger *slog.Logger) *Manager {
	return &Manager{
		repo:     repo,
		logger:   logger,
		handlers: map[string]Handler{},
		entries:  map[string]robfigcron.EntryID{},
		running:  map[string]bool{},
	}
}

func (m *Manager) RegisterHandler(name string, handler Handler) error {
	if name == "" {
		return fmt.Errorf("cron handler name is empty")
	}
	if handler == nil {
		return fmt.Errorf("cron handler %q is nil", name)
	}
	m.mu.Lock()
	m.handlers[name] = handler
	started := m.started
	m.mu.Unlock()

	if started {
		return m.reloadEnabled(context.Background())
	}
	return nil
}

func (m *Manager) UpsertJob(ctx context.Context, req UpsertJobRequest) (*storage.CronJob, error) {
	if err := validateUpsertRequest(req); err != nil {
		return nil, err
	}
	job, err := m.repo.Upsert(ctx, req)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	started := m.started
	m.mu.Unlock()
	if started {
		if err := m.scheduleJob(*job); err != nil {
			return nil, err
		}
	}
	return job, nil
}

func (m *Manager) DisableJob(ctx context.Context, name string) error {
	if err := m.repo.DisableByName(ctx, name); err != nil {
		return err
	}
	m.removeEntry(name)
	return nil
}

func (m *Manager) DeleteJob(ctx context.Context, name string) error {
	m.removeEntry(name)
	return m.repo.DeleteByName(ctx, name)
}

func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return nil
	}
	m.scheduler = robfigcron.New()
	m.started = true
	m.mu.Unlock()

	if err := m.reloadEnabled(ctx); err != nil {
		return err
	}
	m.scheduler.Start()
	m.logInfo("cron manager started")
	return nil
}

func (m *Manager) Stop() context.Context {
	m.mu.Lock()
	if !m.started || m.scheduler == nil {
		m.started = false
		m.mu.Unlock()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx
	}

	scheduler := m.scheduler
	m.started = false
	m.scheduler = nil
	m.entries = map[string]robfigcron.EntryID{}
	m.mu.Unlock()
	m.logInfo("cron manager stopping")
	return scheduler.Stop()
}

func (m *Manager) reloadEnabled(ctx context.Context) error {
	jobs, err := m.repo.ListEnabled(ctx)
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if err := m.scheduleJob(job); err != nil {
			m.logWarn("cron job schedule failed", "job", job.Name, "handler", job.Handler, "error", err)
		}
	}
	return nil
}

func (m *Manager) scheduleJob(job storage.CronJob) error {
	m.mu.Lock()
	if !m.started || m.scheduler == nil {
		m.mu.Unlock()
		return nil
	}
	if id, ok := m.entries[job.Name]; ok {
		m.scheduler.Remove(id)
		delete(m.entries, job.Name)
	}
	if !job.Enabled {
		m.mu.Unlock()
		return nil
	}
	if _, ok := m.handlers[job.Handler]; !ok {
		m.mu.Unlock()
		m.logWarn("cron job handler not registered", "job", job.Name, "handler", job.Handler)
		return nil
	}
	scheduler := m.scheduler
	m.mu.Unlock()

	entryID, err := scheduler.AddFunc(job.Schedule, func() {
		m.runJob(job.Name)
	})
	if err != nil {
		return fmt.Errorf("schedule cron job %q: %w", job.Name, err)
	}

	m.mu.Lock()
	m.entries[job.Name] = entryID
	m.mu.Unlock()
	m.logInfo("cron job scheduled", "job", job.Name, "handler", job.Handler, "schedule", job.Schedule)
	return nil
}

func (m *Manager) runJob(name string) {
	ctx := context.Background()
	job, err := m.repo.GetByName(ctx, name)
	if err != nil {
		m.logWarn("cron job load failed", "job", name, "error", err)
		return
	}
	if !job.Enabled {
		m.removeEntry(job.Name)
		return
	}

	m.mu.Lock()
	if m.running[job.Name] {
		m.mu.Unlock()
		m.logWarn("cron job skipped because previous run is still running", "job", job.Name, "handler", job.Handler)
		return
	}
	handler := m.handlers[job.Handler]
	if handler == nil {
		m.mu.Unlock()
		m.logWarn("cron job handler not registered", "job", job.Name, "handler", job.Handler)
		return
	}
	m.running[job.Name] = true
	m.mu.Unlock()

	startedAt := time.Now()
	m.logInfo("cron job started", "job", job.Name, "handler", job.Handler)
	runErr := handler(ctx, *job)
	duration := time.Since(startedAt)

	m.mu.Lock()
	delete(m.running, job.Name)
	entryID := m.entries[job.Name]
	scheduler := m.scheduler
	m.mu.Unlock()

	lastError := ""
	if runErr != nil {
		lastError = runErr.Error()
		m.logWarn("cron job failed", "job", job.Name, "handler", job.Handler, "duration", duration.String(), "error", runErr)
	} else {
		m.logInfo("cron job completed", "job", job.Name, "handler", job.Handler, "duration", duration.String())
	}

	enabled := job.Enabled
	var nextRun *time.Time
	if scheduler != nil && entryID != 0 {
		entry := scheduler.Entry(entryID)
		if !entry.Next.IsZero() {
			next := entry.Next
			nextRun = &next
		}
	}
	if err := m.repo.UpdateRunState(ctx, job.ID, storage.CronJobRunState{
		LastRunAt: startedAt,
		NextRunAt: nextRun,
		RunCount:  job.RunCount + 1,
		LastError: lastError,
		Enabled:   enabled,
		UpdatedAt: time.Now(),
	}); err != nil {
		m.logWarn("cron job state update failed", "job", job.Name, "handler", job.Handler, "error", err)
	}
}

func (m *Manager) removeEntry(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.scheduler == nil {
		delete(m.entries, name)
		return
	}
	if id, ok := m.entries[name]; ok {
		m.scheduler.Remove(id)
		delete(m.entries, name)
	}
}

func validateUpsertRequest(req UpsertJobRequest) error {
	if req.Name == "" {
		return fmt.Errorf("cron job name is empty")
	}
	if req.Handler == "" {
		return fmt.Errorf("cron job handler is empty")
	}
	if req.Schedule == "" {
		return fmt.Errorf("cron job schedule is empty")
	}
	return nil
}

func (m *Manager) logInfo(msg string, attrs ...any) {
	if m.logger != nil {
		m.logger.Info(msg, attrs...)
	}
}

func (m *Manager) logWarn(msg string, attrs ...any) {
	if m.logger != nil {
		m.logger.Warn(msg, attrs...)
	}
}
