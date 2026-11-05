package maintenance

import (
	"context"
	"elbot/internal/contextinfo"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"elbot/internal/background/share"
	"elbot/internal/config"
	globalevents "elbot/internal/events"
)

type Service struct {
	Media interface {
		Cleanup(context.Context) error
	}
	logs interface {
		CleanupOldLogs() error
		RetentionDays() int
	}
	cleanupSessions    func(context.Context, time.Time) (int, error) // 按截止时间清理会话，忙闲保护由提供方负责。
	cleanupChatHistory func(context.Context, time.Time) (int, error) // 按截止时间清理平台历史，返回已删除条数。
	sessionCleanup     config.MaintenanceCleanupConfig
	chatHistoryCleanup config.MaintenanceCleanupConfig
	sandboxRoot        string
	sandboxCleanup     config.MaintenanceCleanupConfig
}

func NewServiceWithConfig(logs interface {
	CleanupOldLogs() error
	RetentionDays() int
}, cleanupChatHistory func(context.Context, time.Time) (int, error), cfg *config.Config, cleanupSessions func(context.Context, time.Time) (int, error)) *Service {
	return &Service{
		cleanupSessions:    cleanupSessions,
		logs:               logs,
		cleanupChatHistory: cleanupChatHistory,
		sessionCleanup:     cfg.Maintenance.SessionCleanup,
		chatHistoryCleanup: cfg.Maintenance.ChatHistoryCleanup,
		sandboxRoot:        cfg.Sandbox.Root,
		sandboxCleanup:     cfg.Maintenance.SandboxCleanup,
	}
}

func (s *Service) RegisterCronHandlers(register func(string, func(context.Context) error)) {
	register("maintenance.log_cleanup", s.RunLogCleanup)
	register("maintenance.session_cleanup", s.RunSessionCleanup)
	register("maintenance.sandbox_cleanup", s.RunSandboxCleanup)
	register("maintenance.media_cleanup", s.Media.Cleanup)
	register("maintenance.chat_history_cleanup", s.RunChatHistoryCleanup)
}

// SetupCron 只配置维护任务，调度启动由 Background 根部负责。
func SetupCron(ctx context.Context, configure func(context.Context, share.MaintenanceJob) error, cfg *config.Config) error {
	for _, job := range []share.MaintenanceJob{
		{Name: "system.maintenance.log_cleanup", Handler: "maintenance.log_cleanup", Schedule: cfg.Maintenance.LogCleanup.Schedule, Enabled: cfg.Maintenance.LogCleanup.Enabled},
		{Name: "system.maintenance.session_cleanup", Handler: "maintenance.session_cleanup", Schedule: cfg.Maintenance.SessionCleanup.Schedule, Enabled: cfg.Maintenance.SessionCleanup.Enabled},
		{Name: "system.maintenance.sandbox_cleanup", Handler: "maintenance.sandbox_cleanup", Schedule: cfg.Maintenance.SandboxCleanup.Schedule, Enabled: cfg.Maintenance.SandboxCleanup.Enabled},
		{Name: "system.maintenance.chat_history_cleanup", Handler: "maintenance.chat_history_cleanup", Schedule: cfg.Maintenance.ChatHistoryCleanup.Schedule, Enabled: cfg.Maintenance.ChatHistoryCleanup.Enabled},
		{Name: "system.maintenance.media_cleanup", Handler: "maintenance.media_cleanup", Schedule: cfg.Maintenance.SandboxCleanup.Schedule, Enabled: true},
	} {
		if err := configure(ctx, job); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) RunLogCleanup(ctx context.Context) (err error) {
	err = s.logs.CleanupOldLogs()
	record := globalevents.LogRecord{Category: globalevents.LogRuntime, Level: slog.LevelInfo, Module: "maintenance", ResultStatus: globalevents.ResultSucceeded, Content: fmt.Sprintf("retention_days=%d", s.logs.RetentionDays())}
	if err != nil {
		record.Level, record.ResultStatus = slog.LevelError, globalevents.ResultFailed
		if globalevents.LogStatus(err) == globalevents.ResultCanceled {
			record.Level, record.ResultStatus = slog.LevelInfo, globalevents.ResultCanceled
		}
		record.Content += fmt.Sprintf("\nerror=%q", err.Error())
	}
	record.EventName = "maintenance.log_cleanup"
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	record.Identity = globalevents.LogIdentityFromExecution(facts)
	globalevents.Log(ctx, record)
	return err
}

func (s *Service) RunSessionCleanup(ctx context.Context) (err error) {
	if !s.sessionCleanup.Enabled {
		return nil
	}
	deleted, err := s.cleanupSessions(ctx, time.Now().AddDate(0, 0, -s.sessionCleanup.RetentionDays))
	record := globalevents.LogRecord{Category: globalevents.LogRuntime, Level: slog.LevelInfo, Module: "maintenance", ResultStatus: globalevents.ResultSucceeded, Content: fmt.Sprintf("deleted=%d\nretention_days=%d", deleted, s.sessionCleanup.RetentionDays)}
	if err != nil {
		record.Level, record.ResultStatus = slog.LevelError, globalevents.ResultFailed
		if globalevents.LogStatus(err) == globalevents.ResultCanceled {
			record.Level, record.ResultStatus = slog.LevelInfo, globalevents.ResultCanceled
		}
		record.Content += fmt.Sprintf("\nerror=%q", err.Error())
	}
	record.EventName = "maintenance.session_cleanup"
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	record.Identity = globalevents.LogIdentityFromExecution(facts)
	globalevents.Log(ctx, record)
	return err
}

func (s *Service) RunChatHistoryCleanup(ctx context.Context) (err error) {
	if !s.chatHistoryCleanup.Enabled {
		return nil
	}
	cutoff := time.Now().AddDate(0, 0, -s.chatHistoryCleanup.RetentionDays)
	deleted, err := s.cleanupChatHistory(ctx, cutoff)
	record := globalevents.LogRecord{Category: globalevents.LogRuntime, Level: slog.LevelInfo, Module: "maintenance", ResultStatus: globalevents.ResultSucceeded, Content: fmt.Sprintf("deleted=%d\nretention_days=%d", deleted, s.chatHistoryCleanup.RetentionDays)}
	if err != nil {
		record.Level, record.ResultStatus = slog.LevelError, globalevents.ResultFailed
		if globalevents.LogStatus(err) == globalevents.ResultCanceled {
			record.Level, record.ResultStatus = slog.LevelInfo, globalevents.ResultCanceled
		}
		record.Content += fmt.Sprintf("\nerror=%q", err.Error())
	}
	record.EventName = "maintenance.chat_history_cleanup"
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	record.Identity = globalevents.LogIdentityFromExecution(facts)
	globalevents.Log(ctx, record)
	return err
}

func (s *Service) RunSandboxCleanup(ctx context.Context) (err error) {
	cutoff := time.Now().AddDate(0, 0, -s.sandboxCleanup.RetentionDays)
	deleted, err := cleanupSandbox(ctx, s.sandboxRoot, cutoff)
	record := globalevents.LogRecord{Category: globalevents.LogRuntime, Level: slog.LevelInfo, Module: "maintenance", ResultStatus: globalevents.ResultSucceeded, Content: fmt.Sprintf("deleted=%d\nretention_days=%d", deleted, s.sandboxCleanup.RetentionDays)}
	if err != nil {
		record.Level, record.ResultStatus = slog.LevelError, globalevents.ResultFailed
		if globalevents.LogStatus(err) == globalevents.ResultCanceled {
			record.Level, record.ResultStatus = slog.LevelInfo, globalevents.ResultCanceled
		}
		record.Content += fmt.Sprintf("\nerror=%q", err.Error())
	}
	record.EventName = "maintenance.sandbox_cleanup"
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	record.Identity = globalevents.LogIdentityFromExecution(facts)
	globalevents.Log(ctx, record)
	return err
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
