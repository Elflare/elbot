package cron

import (
	"context"
	background "elbot/internal/background/share"
	"elbot/internal/contextinfo"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	robfigcron "github.com/robfig/cron/v3"

	globalevents "elbot/internal/events"
)

// Handler supplies the execution result and independent diagnostic body. The
// scheduler consumes these source facts without classifying the original error.
type Handler func(ctx context.Context, job Job) (logResult globalevents.LogResult, err error)

type Manager struct {
	repo JobRepository

	scheduler *robfigcron.Cron

	mu          sync.Mutex
	handlers    map[string]Handler
	entries     map[string]robfigcron.EntryID
	running     map[string]bool
	started     bool
	stopped     bool
	runCtx      context.Context
	cancel      context.CancelFunc
	startDone   chan struct{}
	startErr    error
	stopDone    context.Context
	workers     sync.WaitGroup
	pendingJobs map[string]struct{} // 尚需重新读取的任务名称；变更期间继续收集下一轮。
	refreshWake chan struct{}       // 合并唤醒信号，实际待办保存在 pendingJobs。
}

func NewManager(repo JobRepository) *Manager {
	return &Manager{repo: repo, handlers: map[string]Handler{}, entries: map[string]robfigcron.EntryID{}, running: map[string]bool{}, pendingJobs: map[string]struct{}{}, refreshWake: make(chan struct{}, 1)}
}

func (m *Manager) RegisterHandler(name string, handler Handler) error {
	m.mu.Lock()
	m.handlers[name] = handler
	m.mu.Unlock()
	return nil
}

func (m *Manager) UpsertJob(ctx context.Context, req UpsertJobRequest) (*Job, error) {
	if err := validateUpsertRequest(req); err != nil {
		return nil, err
	}
	nextRun, err := computeNextRunAt(req.Schedule, "", req.Enabled, time.Now())
	if err != nil {
		return nil, err
	}
	req.NextRunAt = nextRun
	job, err := m.repo.Upsert(ctx, req)
	if err != nil {
		return nil, err
	}
	_ = m.RefreshJob(job.Name)
	return job, nil
}

func (m *Manager) DisableJob(ctx context.Context, name string) error {
	if err := m.repo.DisableByName(ctx, name); err != nil {
		return err
	}
	return m.RefreshJob(name)
}

func (m *Manager) DisableJobIfDeliveryToken(ctx context.Context, name, deliveryToken string) (bool, error) {
	disabled, err := m.repo.DisableByNameIfDeliveryToken(ctx, name, deliveryToken)
	if err != nil || !disabled {
		return disabled, err
	}
	_ = m.RefreshJob(name)
	return true, nil
}

func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return context.Canceled
	}
	if m.started {
		done := m.startDone
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			m.mu.Lock()
			defer m.mu.Unlock()
			return m.startErr
		}
	}
	m.scheduler = robfigcron.New()
	m.runCtx, m.cancel = context.WithCancel(ctx)
	m.startDone = make(chan struct{})
	m.started = true
	m.workers.Add(1)
	context.AfterFunc(m.runCtx, func() { m.Stop() })
	runCtx := m.runCtx
	m.mu.Unlock()
	defer m.workers.Done()

	err := m.reloadEnabled(runCtx)
	for err == nil {
		m.mu.Lock()
		err = runCtx.Err()
		if err != nil || m.stopped {
			if err == nil {
				err = context.Canceled
			}
			m.mu.Unlock()
			break
		}
		if len(m.pendingJobs) == 0 {
			// 发布就绪和 worker 所有权必须与停止、接纳处于同一临界区。
			m.workers.Add(1)
			go m.runRefreshes(runCtx)
			m.scheduler.Start()
			m.mu.Unlock()
			break
		}
		pending := m.pendingJobs
		m.pendingJobs = make(map[string]struct{})
		m.mu.Unlock()
		m.refreshPending(runCtx, pending)
	}
	m.mu.Lock()
	m.startErr = err
	close(m.startDone)
	m.mu.Unlock()
	if err != nil {
		m.Stop()
		return err
	}
	{
		record := globalevents.LogRecord{
			Category:     globalevents.LogRuntime,
			Level:        slog.LevelInfo,
			EventName:    "cron_manager_started",
			Module:       "cron",
			Type:         globalevents.ContentText,
			Content:      "cron manager started",
			ContentLimit: 4096,
		}
		facts, _ := contextinfo.ExecutionFromContext(ctx)
		if facts.SessionID != "" {
			record.Identity.SessionID = &facts.SessionID
		}
		if facts.RunID != "" {
			record.Identity.RunID = &facts.RunID
		}
		if facts.Attempt != "" {
			record.Identity.Attempt = &facts.Attempt
		}
		if facts.RequestID != "" {
			record.Identity.RequestID = &facts.RequestID
		}
		if facts.RootRequestID != "" {
			record.Identity.RootRequestID = &facts.RootRequestID
		}
		background.LogBackground(ctx, record)
	}
	return nil
}

func (m *Manager) Stop() context.Context {
	m.mu.Lock()
	if m.stopDone != nil {
		done := m.stopDone
		m.mu.Unlock()
		return done
	}
	m.stopped = true
	m.started = false
	clear(m.pendingJobs)
	if m.cancel != nil {
		m.cancel()
	}
	done, finish := context.WithCancel(context.Background())
	m.stopDone = done
	var schedulerDone context.Context
	if m.scheduler != nil {
		schedulerDone = m.scheduler.Stop()
	}
	m.mu.Unlock()
	{
		record := globalevents.LogRecord{
			Category:     globalevents.LogRuntime,
			Level:        slog.LevelInfo,
			EventName:    "cron_manager_stopping",
			Module:       "cron",
			Type:         globalevents.ContentText,
			Content:      "cron manager stopping",
			ContentLimit: 4096,
		}
		background.LogBackground(context.Background(), record)
	}
	go func() {
		if schedulerDone != nil {
			<-schedulerDone.Done()
		}
		m.workers.Wait()
		finish()
	}()
	return done
}

func (m *Manager) reloadEnabled(ctx context.Context) error {
	jobs, err := m.repo.ListEnabled(ctx)
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if err := m.scheduleJob(ctx, job); err != nil {
			{
				record := globalevents.LogRecord{
					Category:     globalevents.LogRuntime,
					Level:        slog.LevelError,
					EventName:    "cron_job_schedule_failed",
					Module:       "cron",
					Type:         globalevents.ContentText,
					Content:      fmt.Sprintf("cron job schedule failed\njob=%v\nhandler=%v\nerror=%v", job.Name, job.Handler, err),
					ContentLimit: 4096,
					ResultStatus: globalevents.ResultFailed,
				}
				facts, _ := contextinfo.ExecutionFromContext(ctx)
				if facts.SessionID != "" {
					record.Identity.SessionID = &facts.SessionID
				}
				if facts.RunID != "" {
					record.Identity.RunID = &facts.RunID
				}
				if facts.Attempt != "" {
					record.Identity.Attempt = &facts.Attempt
				}
				if facts.RequestID != "" {
					record.Identity.RequestID = &facts.RequestID
				}
				if facts.RootRequestID != "" {
					record.Identity.RootRequestID = &facts.RootRequestID
				}
				background.LogBackground(ctx, record)
			}
		}
	}
	return nil
}

func (m *Manager) scheduleJob(ctx context.Context, job Job) error {
	m.mu.Lock()
	if !m.started || m.stopped || ctx.Err() != nil {
		m.mu.Unlock()
		return ctx.Err()
	}
	if id, ok := m.entries[job.Name]; ok {
		m.scheduler.Remove(id)
		delete(m.entries, job.Name)
	}
	if !job.Enabled {
		m.mu.Unlock()
		return m.updateNextRunAt(ctx, job, nil)
	}
	if _, ok := m.handlers[job.Handler]; !ok {
		m.mu.Unlock()
		{
			record := globalevents.LogRecord{
				Category:     globalevents.LogRuntime,
				Level:        slog.LevelError,
				EventName:    "cron_job_handler_not_registered",
				Module:       "cron",
				Type:         globalevents.ContentText,
				Content:      fmt.Sprintf("cron job handler not registered\njob=%v\nhandler=%v", job.Name, job.Handler),
				ContentLimit: 4096,
				ResultStatus: globalevents.ResultFailed,
			}
			background.LogBackground(context.Background(), record)
		}
		return m.updateNextRunAt(ctx, job, nil)
	}
	nextRun, err := computeNextRunAt(job.Schedule, job.Metadata, true, time.Now())
	if err != nil {
		m.mu.Unlock()
		return fmt.Errorf("schedule cron job %q: %w", job.Name, err)
	}
	if nextRun == nil {
		m.mu.Unlock()
		return m.updateNextRunAt(ctx, job, nil)
	}
	entryID, err := m.scheduler.AddFunc(job.Schedule, func() {
		m.runJob(job.Name)
	})
	if err != nil {
		m.mu.Unlock()
		return fmt.Errorf("schedule cron job %q: %w", job.Name, err)
	}
	m.entries[job.Name] = entryID
	m.mu.Unlock()
	if err := m.updateNextRunAt(ctx, job, nextRun); err != nil {
		return err
	}
	{
		record := globalevents.LogRecord{
			Category:     globalevents.LogRuntime,
			Level:        slog.LevelInfo,
			EventName:    "cron_job_scheduled",
			Module:       "cron",
			Type:         globalevents.ContentText,
			Content:      fmt.Sprintf("cron job scheduled\njob=%v\nhandler=%v\nschedule=%v", job.Name, job.Handler, job.Schedule),
			ContentLimit: 4096,
			ResultStatus: globalevents.ResultSucceeded,
		}
		background.LogBackground(context.Background(), record)
	}
	return nil
}

func (m *Manager) runJob(name string) {
	m.mu.Lock()
	if !m.started || m.stopped || m.runCtx.Err() != nil {
		m.mu.Unlock()
		return
	}
	ctx := m.runCtx

	identity := globalevents.LogIdentity{}
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	if facts.SessionID != "" {
		identity.SessionID = &facts.SessionID
	}
	if facts.RunID != "" {
		identity.RunID = &facts.RunID
	}
	if facts.Attempt != "" {
		identity.Attempt = &facts.Attempt
	}
	if facts.RequestID != "" {
		identity.RequestID = &facts.RequestID
	}
	if facts.RootRequestID != "" {
		identity.RootRequestID = &facts.RootRequestID
	}
	m.workers.Add(1)
	m.mu.Unlock()
	defer m.workers.Done()
	job, err := m.repo.GetByName(ctx, name)
	if err != nil {
		if ctx.Err() == nil {
			{
				record := globalevents.LogRecord{
					Category:     globalevents.LogRuntime,
					Level:        slog.LevelError,
					EventName:    "cron_job_load_failed",
					Module:       "cron",
					Type:         globalevents.ContentText,
					Content:      fmt.Sprintf("cron job load failed\njob=%v\nerror=%v", name, err),
					ContentLimit: 4096,
					ResultStatus: globalevents.ResultFailed,
				}
				record.Identity = identity
				background.LogBackground(ctx, record)
			}
		}
		return
	}
	if !job.Enabled {
		m.removeEntry(job.Name)
		return
	}

	m.mu.Lock()
	if m.stopped || ctx.Err() != nil {
		m.mu.Unlock()
		return
	}
	if m.running[job.Name] {
		m.mu.Unlock()
		{
			record := globalevents.LogRecord{
				Category:     globalevents.LogRuntime,
				Level:        slog.LevelWarn,
				EventName:    "cron_job_skipped_because_previous_run_is_still_running",
				Module:       "cron",
				Type:         globalevents.ContentText,
				Content:      fmt.Sprintf("cron job skipped because previous run is still running\njob=%v\nhandler=%v", job.Name, job.Handler),
				ContentLimit: 4096,
				ResultStatus: globalevents.ResultSkipped,
			}
			record.Identity = identity
			background.LogBackground(ctx, record)
		}
		return
	}
	handler := m.handlers[job.Handler]
	if handler == nil {
		m.mu.Unlock()
		{
			record := globalevents.LogRecord{
				Category:     globalevents.LogRuntime,
				Level:        slog.LevelError,
				EventName:    "cron_job_handler_not_registered",
				Module:       "cron",
				Type:         globalevents.ContentText,
				Content:      fmt.Sprintf("cron job handler not registered\njob=%v\nhandler=%v", job.Name, job.Handler),
				ContentLimit: 4096,
				ResultStatus: globalevents.ResultFailed,
			}
			record.Identity = identity
			background.LogBackground(ctx, record)
		}
		return
	}
	m.running[job.Name] = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.running, job.Name)
		m.mu.Unlock()
	}()

	startedAt := time.Now()
	{
		record := globalevents.LogRecord{
			Category:     globalevents.LogRuntime,
			Level:        slog.LevelInfo,
			EventName:    "cron_job_started",
			Module:       "cron",
			Type:         globalevents.ContentText,
			Content:      fmt.Sprintf("cron job started\njob=%v\nhandler=%v", job.Name, job.Handler),
			ContentLimit: 4096,
		}
		record.Identity = identity
		background.LogBackground(ctx, record)
	}
	logResult, _ := handler(ctx, *job)
	resultStatus := logResult.ResultStatus
	duration := time.Since(startedAt)
	// Deadline expiry is a failure; only explicit cancellation is canceled in logs.
	record := globalevents.LogRecord{
		Category: globalevents.LogRuntime, Level: slog.LevelInfo,
		EventName: "cron_job_completed", Module: "cron", Type: globalevents.ContentText,
		Content:      fmt.Sprintf("cron job finished\njob=%s\nhandler=%s\nduration=%s", job.Name, job.Handler, duration),
		ContentLimit: 4096, ResultStatus: resultStatus,
	}
	for _, line := range strings.Split(logResult.Content, "\n") {
		if strings.TrimSpace(line) != "" {
			record.Content += "\n" + line
		}
	}
	if resultStatus == globalevents.ResultFailed {
		record.Level = slog.LevelError
	}
	record.Identity = identity
	defer func() {
		if record.ResultStatus == globalevents.ResultFailed {
			record.EventName = "cron_job_failed"
		}
		record.EventName += "." + job.Handler
		background.LogBackground(ctx, record)
	}()
	// Final bookkeeping remains part of the tracked invocation after cancel.
	ctx = context.WithoutCancel(ctx)

	defer m.RefreshJob(job.Name)
	if err := m.repo.UpdateRunState(ctx, job.ID, JobRunState{
		LastRunAt: startedAt,
		UpdatedAt: time.Now(),
	}); err != nil {
		record.Content += fmt.Sprintf("\nstate_save_error=%q", err.Error())
		record.ResultStatus = globalevents.ResultFailed
		record.Level = slog.LevelError
		{
			record := globalevents.LogRecord{
				Category:     globalevents.LogRuntime,
				Level:        slog.LevelError,
				EventName:    "cron_job_state_update_failed",
				Module:       "cron",
				Type:         globalevents.ContentText,
				Content:      fmt.Sprintf("cron job state update failed\njob=%v\nhandler=%v\nerror=%v", job.Name, job.Handler, err),
				ContentLimit: 4096,
				ResultStatus: globalevents.ResultFailed,
			}
			record.Identity = identity
			background.LogBackground(ctx, record)
		}
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

func (m *Manager) updateNextRunAt(ctx context.Context, job Job, nextRunAt *time.Time) error {
	if job.NextRunAt == nil && nextRunAt == nil ||
		job.NextRunAt != nil && nextRunAt != nil && job.NextRunAt.Equal(*nextRunAt) {
		return nil
	}
	if err := m.repo.UpdateNextRunAt(ctx, job.ID, nextRunAt, time.Now()); err != nil {
		{
			record := globalevents.LogRecord{
				Category:     globalevents.LogRuntime,
				Level:        slog.LevelError,
				EventName:    "cron_job_next_run_update_failed",
				Module:       "cron",
				Type:         globalevents.ContentText,
				Content:      fmt.Sprintf("cron job next run update failed\njob=%v\nhandler=%v\nerror=%v", job.Name, job.Handler, err),
				ContentLimit: 4096,
				ResultStatus: globalevents.ResultFailed,
			}
			facts, _ := contextinfo.ExecutionFromContext(ctx)
			if facts.SessionID != "" {
				record.Identity.SessionID = &facts.SessionID
			}
			if facts.RunID != "" {
				record.Identity.RunID = &facts.RunID
			}
			if facts.Attempt != "" {
				record.Identity.Attempt = &facts.Attempt
			}
			if facts.RequestID != "" {
				record.Identity.RequestID = &facts.RequestID
			}
			if facts.RootRequestID != "" {
				record.Identity.RootRequestID = &facts.RootRequestID
			}
			background.LogBackground(ctx, record)
		}
		return err
	}
	return nil
}

func computeNextRunAt(schedule, metadata string, enabled bool, now time.Time) (*time.Time, error) {
	parsed, err := robfigcron.ParseStandard(schedule)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, nil
	}
	if runAt, ok, err := userOnceRunAt(metadata); ok || err != nil {
		if err != nil {
			return nil, err
		}
		if runAt.After(now) {
			return timePtr(runAt), nil
		}
		return nil, nil
	}
	next := parsed.Next(now)
	if next.IsZero() {
		return nil, nil
	}
	return timePtr(next), nil
}

func userOnceRunAt(metadata string) (time.Time, bool, error) {
	if metadata == "" {
		return time.Time{}, false, nil
	}
	var meta Metadata
	if err := json.Unmarshal([]byte(metadata), &meta); err != nil {
		return time.Time{}, false, err
	}
	if meta.Kind != metadataKind || meta.Schedule.Mode != ScheduleOnce {
		return time.Time{}, false, nil
	}
	runAt, err := parseRunAt(meta.Schedule.RunAt)
	if err != nil {
		return time.Time{}, true, err
	}
	return runAt, true, nil
}

func timePtr(t time.Time) *time.Time {
	v := t
	return &v
}
