package maintenance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	elcron "elbot/internal/background/cron"
	"elbot/internal/config"
	globalevents "elbot/internal/events"
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
	chatHistory        storage.ChatHistoryRepository
	sessionCleanup     config.MaintenanceCleanupConfig
	chatHistoryCleanup config.ChatHistoryCleanupConfig
	sandboxRoot        string
	sandboxCleanup     config.MaintenanceCleanupConfig
}

func NewServiceWithConfig(logs interface {
	CleanupOldLogs() error
	RetentionDays() int
}, chatHistory storage.ChatHistoryRepository, cfg *config.Config, sessions *session.Service) *Service {
	return &Service{
		Sessions:           sessions,
		logs:               logs,
		chatHistory:        chatHistory,
		sessionCleanup:     cfg.Maintenance.SessionCleanup,
		chatHistoryCleanup: cfg.Maintenance.ChatHistoryCleanup,
		sandboxRoot:        cfg.Sandbox.Root,
		sandboxCleanup:     cfg.Maintenance.SandboxCleanup,
	}
}

func (s *Service) RegisterCronHandlers(manager *elcron.Manager) error {
	if err := manager.RegisterHandler("maintenance.log_cleanup", func(ctx context.Context, job storage.CronJob) (globalevents.LogResult, error) {
		return s.RunLogCleanup(ctx)
	}); err != nil {
		return err
	}
	if err := manager.RegisterHandler("maintenance.session_cleanup", func(ctx context.Context, job storage.CronJob) (globalevents.LogResult, error) {

		return s.RunSessionCleanup(ctx)
	}); err != nil {
		return err
	}
	if err := manager.RegisterHandler("maintenance.sandbox_cleanup", func(ctx context.Context, job storage.CronJob) (globalevents.LogResult, error) {

		return s.RunSandboxCleanup(ctx)
	}); err != nil {
		return err
	}
	if err := manager.RegisterHandler("maintenance.media_cleanup", func(ctx context.Context, job storage.CronJob) (globalevents.LogResult, error) {

		return s.Media.Cleanup(ctx)
	}); err != nil {
		return err
	}
	if err := manager.RegisterHandler("maintenance.chat_history_cleanup", func(ctx context.Context, job storage.CronJob) (globalevents.LogResult, error) {

		return s.RunChatHistoryCleanup(ctx)
	}); err != nil {
		return err
	}
	return nil
}

func SetupCron(ctx context.Context, manager *elcron.Manager, cfg *config.Config) error {
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
	if !s.chatHistoryCleanup.Enabled {
		return globalevents.LogResult{ResultStatus: globalevents.ResultSkipped, Content: "cleanup is not enabled or configured"}, nil
	}
	cutoff := time.Now().AddDate(0, 0, -s.chatHistoryCleanup.RetentionDays)
	deleted, err := s.chatHistory.DeleteBefore(ctx, cutoff)
	logResult = globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: fmt.Sprintf("deleted=%d\nretention_days=%d", deleted, s.chatHistoryCleanup.RetentionDays)}
	if err == nil {
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
	return deleted, removeEmptyDirs(dir)
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
	var errs []error
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := os.Remove(dirs[i]); err != nil && !errors.Is(err, syscall.ENOTEMPTY) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
