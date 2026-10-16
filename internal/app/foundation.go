package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"elbot/internal/config"
	globalevents "elbot/internal/events"
	"elbot/internal/logging"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

type defaultFoundationFactory struct{}

func (defaultFoundationFactory) Build(ctx context.Context, req FoundationRequest) (_ *FoundationComponents, err error) {
	cfg, err := config.Load(req.Options.ConfigPath)
	if err != nil {
		return nil, err
	}
	req.Profiler.SetEnabled(startupProfileEnabled(cfg.Runtime.LogLevel))
	req.Profiler.Mark("config.Load")

	logs, err := logging.NewManager(cfg.Runtime.LogLevel, cfg.Storage.SessionsSQLitePath, cfg.Runtime.LogRetentionDays)
	if err != nil {
		return nil, err
	}
	lifecycle := &foundationLifecycle{logs: logs}
	partial := &FoundationComponents{Logs: logs, Lifecycle: lifecycle}

	req.Profiler.Mark("logging.NewManager")
	logStartupConfiguration(req.Options, cfg)
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

	return &FoundationComponents{
		Config:           cfg,
		Logs:             logs,
		Store:            store,
		ChatHistoryStore: chatHistoryStore,
		ChatHistory:      chatHistory,
		Lifecycle:        lifecycle,
	}, nil
}

func logStartupConfiguration(opts Options, cfg *config.Config) {
	{
		record := globalevents.LogRecord{
			Category:     globalevents.LogRuntime,
			Level:        slog.LevelInfo,
			EventName:    "elbot_started",
			Module:       "app",
			Type:         globalevents.ContentText,
			Content:      fmt.Sprintf("elbot started\nversion=%v\nconfig_path=%v\nproviders_config_path=%v\nstate_config_path=%v\nelnis_config_path=%v\nwork_provider=%v\nwork_model=%v\nchat_provider=%v\nchat_model=%v\nsoul_path=%v\nsessions_sqlite_path=%v\nchat_history_sqlite_path=%v", opts.Version, cfg.ConfigPath, cfg.ProvidersConfigPath, cfg.StateConfigPath, cfg.ElnisConfigPath, cfg.ModeModels["work"].Provider, cfg.ModeModels["work"].Model, cfg.ModeModels["chat"].Provider, cfg.ModeModels["chat"].Model, cfg.Soul.Path, cfg.Storage.SessionsSQLitePath, cfg.Storage.ChatHistorySQLitePath),
			ContentLimit: 4096,
			ResultStatus: globalevents.ResultSucceeded,
		}
		_ = globalevents.EmitLog(context.Background(), record)
	}
}

type foundationLifecycle struct {
	logs             LogManager
	store            storage.Store
	chatHistoryStore ChatHistoryStore
}

func (l *foundationLifecycle) Close(ctx context.Context) error {
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
		if err := l.logs.Close(ctx); err != nil {
			errs = append(errs, fmt.Errorf("close logs: %w", err))
		}
	}
	return errors.Join(errs...)
}
