package maintenance

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"elbot/internal/config"
	elcron "elbot/internal/cron"
	"elbot/internal/logging"
	"elbot/internal/session"
	"elbot/internal/storage"
)

type Service struct {
	logs               *logging.Manager
	store              storage.Store
	chatHistory        storage.ChatHistoryRepository
	sessionCleanup     config.SessionCleanupConfig
	chatHistoryCleanup config.ChatHistoryCleanupConfig
	sandboxRoot        string
	sandboxCleanup     config.MaintenanceCleanupConfig
	logger             *slog.Logger
}

func NewService(logs *logging.Manager, store storage.Store, sessionCleanup config.SessionCleanupConfig, logger *slog.Logger) *Service {
	return &Service{logs: logs, store: store, sessionCleanup: sessionCleanup, logger: logger}
}

func NewServiceWithConfig(logs *logging.Manager, store storage.Store, chatHistory storage.ChatHistoryRepository, cfg *config.Config, logger *slog.Logger) *Service {
	if cfg == nil {
		return NewService(logs, store, config.SessionCleanupConfig{}, logger)
	}
	return &Service{logs: logs, store: store, chatHistory: chatHistory, sessionCleanup: cfg.Session.Cleanup, chatHistoryCleanup: cfg.Maintenance.ChatHistoryCleanup, sandboxRoot: cfg.Sandbox.Root, sandboxCleanup: cfg.Maintenance.SandboxCleanup, logger: logger}
}

func (s *Service) RegisterCronHandlers(manager *elcron.Manager) error {
	if manager == nil {
		return nil
	}
	if err := manager.RegisterHandler("maintenance.log_cleanup", func(ctx context.Context, job storage.CronJob) error {
		return s.RunLogCleanup(ctx)
	}); err != nil {
		return err
	}
	if err := manager.RegisterHandler("maintenance.session_cleanup", func(ctx context.Context, job storage.CronJob) error {
		return s.RunSessionCleanup(ctx)
	}); err != nil {
		return err
	}
	if err := manager.RegisterHandler("maintenance.sandbox_cleanup", func(ctx context.Context, job storage.CronJob) error {
		return s.RunSandboxCleanup(ctx)
	}); err != nil {
		return err
	}
	if err := manager.RegisterHandler("maintenance.chat_history_cleanup", func(ctx context.Context, job storage.CronJob) error {
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
	if err := upsertOrDisable(ctx, manager, cfg.Session.Cleanup.Enabled, "system.maintenance.session_cleanup", "maintenance.session_cleanup", cfg.Maintenance.LogCleanup.Schedule); err != nil {
		return err
	}
	if err := upsertOrDisable(ctx, manager, cfg.Maintenance.SandboxCleanup.Enabled, "system.maintenance.sandbox_cleanup", "maintenance.sandbox_cleanup", cfg.Maintenance.SandboxCleanup.Schedule); err != nil {
		return err
	}
	if err := upsertOrDisable(ctx, manager, cfg.Maintenance.ChatHistoryCleanup.Enabled, "system.maintenance.chat_history_cleanup", "maintenance.chat_history_cleanup", cfg.Maintenance.ChatHistoryCleanup.Schedule); err != nil {
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

func (s *Service) RunLogCleanup(ctx context.Context) error {
	if err := s.logs.CleanupOldLogs(); err != nil {
		s.warn("maintenance log cleanup failed", "error", err)
		return err
	}
	s.info("maintenance log cleanup completed", "retention_days", s.logs.RetentionDays())
	return nil
}

func (s *Service) RunSessionCleanup(ctx context.Context) error {
	if !s.sessionCleanup.Enabled {
		return nil
	}
	deleted, err := session.NewService(s.store).CleanupExpired(ctx, time.Now().AddDate(0, 0, -s.sessionCleanup.RetentionDays))
	if err != nil {
		s.warn("maintenance session cleanup failed", "error", err, "retention_days", s.sessionCleanup.RetentionDays)
		return err
	}
	s.info("maintenance session cleanup completed", "deleted", deleted, "retention_days", s.sessionCleanup.RetentionDays)
	return nil
}

func (s *Service) RunChatHistoryCleanup(ctx context.Context) error {
	if !s.chatHistoryCleanup.Enabled || s.chatHistoryCleanup.RetentionDays <= 0 || s.chatHistory == nil {
		return nil
	}
	cutoff := time.Now().AddDate(0, 0, -s.chatHistoryCleanup.RetentionDays)
	deleted, err := s.chatHistory.DeleteBefore(ctx, cutoff)
	if err != nil {
		s.warn("maintenance chat history cleanup failed", "error", err, "retention_days", s.chatHistoryCleanup.RetentionDays)
		return err
	}
	s.info("maintenance chat history cleanup completed", "deleted", deleted, "retention_days", s.chatHistoryCleanup.RetentionDays)
	return nil
}

func (s *Service) RunSandboxCleanup(ctx context.Context) error {
	if s.sandboxRoot == "" || s.sandboxCleanup.RetentionDays <= 0 {
		return nil
	}
	cutoff := time.Now().AddDate(0, 0, -s.sandboxCleanup.RetentionDays)
	deleted, err := cleanupSandbox(ctx, s.sandboxRoot, cutoff)
	if err != nil {
		s.warn("maintenance sandbox cleanup failed", "error", err, "retention_days", s.sandboxCleanup.RetentionDays)
		return err
	}
	s.info("maintenance sandbox cleanup completed", "deleted", deleted, "retention_days", s.sandboxCleanup.RetentionDays)
	return nil
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

func (s *Service) info(msg string, attrs ...any) {
	if s.logger != nil {
		s.logger.Info(msg, attrs...)
	}
}

func (s *Service) warn(msg string, attrs ...any) {
	if s.logger != nil {
		s.logger.Warn(msg, attrs...)
	}
}
