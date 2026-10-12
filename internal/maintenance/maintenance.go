package maintenance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"elbot/internal/config"
	elcron "elbot/internal/cron"
	globalevents "elbot/internal/events"
	"elbot/internal/logging"
	"elbot/internal/media"
	"elbot/internal/session"
	"elbot/internal/storage"
)

type Service struct {
	Sessions *session.Service
	Media    *media.Manager
	logs     interface {
		CleanupOldLogs() error
		RetentionDays() int
	}
	store              storage.Store
	chatHistory        storage.ChatHistoryRepository
	sessionCleanup     config.MaintenanceCleanupConfig
	chatHistoryCleanup config.ChatHistoryCleanupConfig
	sandboxRoot        string
	sandboxCleanup     config.MaintenanceCleanupConfig
}

func NewService(logs *logging.Manager, store storage.Store, sessionCleanup config.MaintenanceCleanupConfig) *Service {
	return &Service{Sessions: session.NewService(store), logs: logs, store: store, sessionCleanup: sessionCleanup}
}

func NewServiceWithConfig(logs interface {
	CleanupOldLogs() error
	RetentionDays() int
}, store storage.Store, chatHistory storage.ChatHistoryRepository, cfg *config.Config, sessions *session.Service) *Service {
	if cfg == nil {
		return &Service{Sessions: sessions, logs: logs, store: store}
	}
	return &Service{
		Sessions:           sessions,
		logs:               logs,
		store:              store,
		chatHistory:        chatHistory,
		sessionCleanup:     cfg.Maintenance.SessionCleanup,
		chatHistoryCleanup: cfg.Maintenance.ChatHistoryCleanup,
		sandboxRoot:        cfg.Sandbox.Root,
		sandboxCleanup:     cfg.Maintenance.SandboxCleanup,
	}
}

func (s *Service) RegisterCronHandlers(manager *elcron.Manager) error {
	if manager == nil {
		return nil
	}
	if err := manager.RegisterHandler("maintenance.log_cleanup", func(ctx context.Context, job storage.CronJob) (globalevents.LogResult, error) {
		return s.RunLogCleanup(ctx)
	}); err != nil {
		return err
	}
	if err := manager.RegisterHandler("maintenance.session_cleanup", func(ctx context.Context, job storage.CronJob) (globalevents.LogResult, error) {
		if !s.sessionCleanup.Enabled {
			return globalevents.LogResult{ResultStatus: globalevents.ResultSkipped, Content: "cleanup is not enabled or configured"}, nil
		}
		return s.RunSessionCleanup(ctx)
	}); err != nil {
		return err
	}
	if err := manager.RegisterHandler("maintenance.sandbox_cleanup", func(ctx context.Context, job storage.CronJob) (globalevents.LogResult, error) {
		if s.sandboxRoot == "" || s.sandboxCleanup.RetentionDays <= 0 {
			return globalevents.LogResult{ResultStatus: globalevents.ResultSkipped, Content: "cleanup is not enabled or configured"}, nil
		}
		return s.RunSandboxCleanup(ctx)
	}); err != nil {
		return err
	}
	if err := manager.RegisterHandler("maintenance.media_cleanup", func(ctx context.Context, job storage.CronJob) (globalevents.LogResult, error) {
		if s.Media == nil {
			return globalevents.LogResult{ResultStatus: globalevents.ResultSkipped, Content: "cleanup is not enabled or configured"}, nil
		}
		return s.Media.Cleanup(ctx)
	}); err != nil {
		return err
	}
	if err := manager.RegisterHandler("maintenance.chat_history_cleanup", func(ctx context.Context, job storage.CronJob) (globalevents.LogResult, error) {
		if !s.chatHistoryCleanup.Enabled || s.chatHistoryCleanup.RetentionDays <= 0 || s.chatHistory == nil {
			return globalevents.LogResult{ResultStatus: globalevents.ResultSkipped, Content: "cleanup is not enabled or configured"}, nil
		}
		return s.RunChatHistoryCleanup(ctx)
	}); err != nil {
		return err
	}
	return nil
}

func SetupCron(ctx context.Context, manager *elcron.Manager, cfg *config.Config) error {
	if manager == nil || cfg == nil {
		return nil
	}
	if err := upsertOrDisable(ctx, manager, cfg.Maintenance.LogCleanup.Enabled, "system.maintenance.log_cleanup", "maintenance.log_cleanup", cfg.Maintenance.LogCleanup.Schedule); err != nil {
		return err
	}
	if err := upsertOrDisable(ctx, manager, cfg.Maintenance.SessionCleanup.Enabled, "system.maintenance.session_cleanup", "maintenance.session_cleanup", cfg.Maintenance.SessionCleanup.Schedule); err != nil {
		return err
	}
	if err := upsertOrDisable(ctx, manager, cfg.Maintenance.SandboxCleanup.Enabled, "system.maintenance.sandbox_cleanup", "maintenance.sandbox_cleanup", cfg.Maintenance.SandboxCleanup.Schedule); err != nil {
		return err
	}
	if err := upsertOrDisable(ctx, manager, cfg.Maintenance.ChatHistoryCleanup.Enabled, "system.maintenance.chat_history_cleanup", "maintenance.chat_history_cleanup", cfg.Maintenance.ChatHistoryCleanup.Schedule); err != nil {
		return err
	}
	if err := upsertOrDisable(ctx, manager, true, "system.maintenance.media_cleanup", "maintenance.media_cleanup", cfg.Maintenance.SandboxCleanup.Schedule); err != nil {
		return err
	}
	return manager.Start(ctx)
}

func upsertOrDisable(ctx context.Context, manager *elcron.Manager, enabled bool, name, handler, schedule string) error {
	if enabled {
		_, err := manager.UpsertJob(ctx, elcron.UpsertJobRequest{Name: name, Handler: handler, Schedule: schedule, Enabled: true})
		return err
	}
	if err := manager.DisableJob(ctx, name); err != nil && !errors.Is(err, storage.ErrNotFound) {
		return err
	}
	return nil
}

func (s *Service) RunLogCleanup(ctx context.Context) (logResult globalevents.LogResult, err error) {
	err = s.logs.CleanupOldLogs()
	logResult = globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: fmt.Sprintf("retention_days=%d", s.logs.RetentionDays())}
	if err != nil {
		logResult.ResultStatus = globalevents.ResultFailed
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = globalevents.ResultCanceled
		}
		logResult.Content += fmt.Sprintf("\nerror=%q", err.Error())
	}
	return logResult, err
}

func (s *Service) RunSessionCleanup(ctx context.Context) (logResult globalevents.LogResult, err error) {
	if !s.sessionCleanup.Enabled {
		return globalevents.LogResult{ResultStatus: globalevents.ResultSkipped, Content: "cleanup is not enabled or configured"}, nil
	}
	deleted, err := s.Sessions.CleanupExpired(ctx, time.Now().AddDate(0, 0, -s.sessionCleanup.RetentionDays))
	logResult = globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: fmt.Sprintf("deleted=%d\nretention_days=%d", deleted, s.sessionCleanup.RetentionDays)}
	if err != nil {
		logResult.ResultStatus = globalevents.ResultFailed
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = globalevents.ResultCanceled
		}
		logResult.Content += fmt.Sprintf("\nerror=%q", err.Error())
	}
	return logResult, err
}

func (s *Service) RunChatHistoryCleanup(ctx context.Context) (logResult globalevents.LogResult, err error) {
	if !s.chatHistoryCleanup.Enabled || s.chatHistoryCleanup.RetentionDays <= 0 || s.chatHistory == nil {
		return globalevents.LogResult{ResultStatus: globalevents.ResultSkipped, Content: "cleanup is not enabled or configured"}, nil
	}
	cutoff := time.Now().AddDate(0, 0, -s.chatHistoryCleanup.RetentionDays)
	deleted, err := s.chatHistory.DeleteBefore(ctx, cutoff)
	logResult = globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: fmt.Sprintf("deleted=%d\nretention_days=%d", deleted, s.chatHistoryCleanup.RetentionDays)}
	if err == nil && s.Media != nil {
		err = s.Media.ReconcileHistory(ctx)
	}
	if err != nil {
		logResult.ResultStatus = globalevents.ResultFailed
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = globalevents.ResultCanceled
		}
		logResult.Content += fmt.Sprintf("\nerror=%q", err.Error())
	}
	return logResult, err
}

func (s *Service) RunSandboxCleanup(ctx context.Context) (logResult globalevents.LogResult, err error) {
	if s.sandboxRoot == "" || s.sandboxCleanup.RetentionDays <= 0 {
		return globalevents.LogResult{ResultStatus: globalevents.ResultSkipped, Content: "cleanup is not enabled or configured"}, nil
	}
	cutoff := time.Now().AddDate(0, 0, -s.sandboxCleanup.RetentionDays)
	deleted, err := cleanupSandbox(ctx, s.sandboxRoot, cutoff)
	logResult = globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: fmt.Sprintf("deleted=%d\nretention_days=%d", deleted, s.sandboxCleanup.RetentionDays)}
	if err != nil {
		logResult.ResultStatus = globalevents.ResultFailed
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = globalevents.ResultCanceled
		}
		logResult.Content += fmt.Sprintf("\nerror=%q", err.Error())
	}
	return logResult, err
}

func cleanupSandbox(ctx context.Context, dir string, cutoff time.Time) (int, error) {
	deleted := 0
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || path == dir {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if info.ModTime().Before(cutoff) {
			if err := os.Remove(path); err != nil {
				return err
			}
			deleted++
		}
		return nil
	})
	if err != nil {
		return deleted, err
	}
	_ = removeEmptyDirs(dir)
	return deleted, nil
}

func removeEmptyDirs(root string) error {
	dirs := []string{}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || path == root || !entry.IsDir() {
			return err
		}
		dirs = append(dirs, path)
		return nil
	}); err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = os.Remove(dirs[i])
	}
	return nil
}
