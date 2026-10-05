package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"

	"elbot/internal/config"
	elcron "elbot/internal/cron"
	"elbot/internal/logging"
	"elbot/internal/maintenance"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
	"elbot/internal/tool/builtin"
)

type defaultFoundationFactory struct{}

func (defaultFoundationFactory) Build(ctx context.Context, req FoundationRequest) (_ *FoundationComponents, err error) {
	cfg, err := config.Load(req.Options.ConfigPath)
	if err != nil {
		return nil, err
	}
	req.Profiler.SetEnabled(startupProfileEnabled(cfg.Runtime.LogLevel))
	req.Profiler.Mark("config.Load")
	ctx = builtin.WithConfigEnvDir(ctx, filepath.Dir(cfg.ConfigPath))

	logs, err := logging.NewManager(cfg.Runtime.LogLevel, cfg.Storage.SessionsSQLitePath, cfg.Runtime.LogRetentionDays)
	if err != nil {
		return nil, err
	}
	lifecycle := &foundationLifecycle{cfg: cfg, logs: logs}
	partial := &FoundationComponents{Lifecycle: lifecycle, StopCron: lifecycle.StopCron}

	req.Profiler.Mark("logging.NewManager")
	logger := logs.Runtime()
	logStartupConfiguration(logger, req.Options, cfg)
	if err = config.FirstError(cfg.ValidateModelProviders("work")); err != nil {
		return partial, err
	}

	store, err := sqlite.New(ctx, cfg.Storage.SessionsSQLitePath)
	if err != nil {
		return partial, err
	}
	lifecycle.store = store
	req.Profiler.Mark("sqlite.New")

	chatHistoryStore, err := sqlite.NewChatHistory(ctx, cfg.Storage.ChatHistorySQLitePath)
	if err != nil {
		return partial, err
	}
	lifecycle.chatHistoryStore = chatHistoryStore
	req.Profiler.Mark("chat history sqlite.New")
	chatHistory := chatHistoryStore.Repository()

	maint := maintenance.NewServiceWithConfig(logs, store, chatHistory, cfg, logger)
	cronManager := elcron.NewManager(store.CronJobs(), logger)
	lifecycle.cronManager = cronManager
	if err = maint.RegisterCronHandlers(cronManager); err != nil {
		return partial, err
	}
	req.Profiler.Mark("cron async prepared")

	return &FoundationComponents{
		Maintenance:      maint,
		Config:           cfg,
		Logs:             logs,
		Logger:           logger,
		Store:            store,
		ChatHistoryStore: chatHistoryStore,
		ChatHistory:      chatHistory,
		CronManager:      cronManager,
		StartCron:        lifecycle.startCron,
		StopCron:         lifecycle.StopCron,
		Lifecycle:        lifecycle,
	}, nil
}

func logStartupConfiguration(logger *slog.Logger, opts Options, cfg *config.Config) {
	logger.Info("elbot started",
		"version", opts.Version,
		"config_path", cfg.ConfigPath,
		"providers_config_path", cfg.ProvidersConfigPath,
		"state_config_path", cfg.StateConfigPath,
		"elnis_config_path", cfg.ElnisConfigPath,
		"work_provider", cfg.ModeModels["work"].Provider,
		"work_model", cfg.ModeModels["work"].Model,
		"chat_provider", cfg.ModeModels["chat"].Provider,
		"chat_model", cfg.ModeModels["chat"].Model,
		"soul_path", cfg.Soul.Path,
		"sessions_sqlite_path", cfg.Storage.SessionsSQLitePath,
		"chat_history_sqlite_path", cfg.Storage.ChatHistorySQLitePath,
	)
}

type foundationLifecycle struct {
	cfg              *config.Config
	logs             LogManager
	store            storage.Store
	chatHistoryStore ChatHistoryStore
	cronManager      *elcron.Manager
	cronStartupDone  chan struct{}
	cronScheduled    bool
	cronMu           sync.Mutex
	cronStopped      bool
	cronCancel       context.CancelFunc
}

func (l *foundationLifecycle) startCron(ctx context.Context, service *elcron.Service) {
	l.cronMu.Lock()
	defer l.cronMu.Unlock()
	if l.cronManager == nil || service == nil || l.cronScheduled || l.cronStopped {
		return
	}
	ctx, l.cronCancel = context.WithCancel(ctx)
	l.cronScheduled = true
	l.cronStartupDone = make(chan struct{})
	startCronAsync(ctx, l.cronManager, service, l.cfg, l.logs.Runtime(), l.cronStartupDone)
}

// StopCron stops the producer before Runner releases runtime/Hook dependencies.
// Every caller waits on the same startup and manager completion signals.
func (l *foundationLifecycle) StopCron(ctx context.Context) error {
	l.cronMu.Lock()
	l.cronStopped = true
	if l.cronCancel != nil {
		l.cronCancel()
	}
	startupDone := l.cronStartupDone
	var stopped context.Context
	if l.cronManager != nil {
		stopped = l.cronManager.Stop()
	}
	l.cronMu.Unlock()
	if startupDone != nil {
		select {
		case <-startupDone:
		case <-ctx.Done():
			return fmt.Errorf("wait cron startup: %w", ctx.Err())
		}
	}
	if stopped != nil {
		select {
		case <-stopped.Done():
		case <-ctx.Done():
			return fmt.Errorf("stop cron manager: %w", ctx.Err())
		}
	}
	return nil
}

func (l *foundationLifecycle) Close(ctx context.Context) error {
	if err := l.StopCron(ctx); err != nil {
		return err
	}
	var errs []error
	if l.chatHistoryStore != nil {
		if err := l.chatHistoryStore.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close chat history store: %w", err))
		}
	}
	if l.store != nil {
		if err := l.store.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close main store: %w", err))
		}
	}
	if l.logs != nil {
		if err := l.logs.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close logs: %w", err))
		}
	}
	return errors.Join(errs...)
}
