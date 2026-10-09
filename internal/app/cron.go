package app

import (
	"context"
	"elbot/internal/contextinfo"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"elbot/internal/config"
	elcron "elbot/internal/cron"
	globalevents "elbot/internal/events"
	"elbot/internal/maintenance"
)

func startCronAsync(ctx context.Context, manager *elcron.Manager, service *elcron.Service, cfg *config.Config, done chan<- struct{}) {
	go func() {
		defer close(done)
		startedAt := time.Now()
		if err := setupCron(ctx, manager, cfg); err != nil {
			if !errors.Is(err, context.Canceled) {
				{
					record := globalevents.LogRecord{
						Category:     globalevents.LogRuntime,
						Level:        slog.LevelError,
						EventName:    "cron_async_startup_failed",
						Module:       "app",
						Type:         globalevents.ContentText,
						Content:      fmt.Sprintf("cron async startup failed\nduration=%v\nerror=%v", time.Since(startedAt).String(), err.Error()),
						ContentLimit: 4096,
						ResultStatus: globalevents.ResultFailed,
					}
					if errors.Is(err, context.Canceled) {
						record.ResultStatus = globalevents.ResultCanceled
						record.Level = slog.LevelInfo
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
					_ = globalevents.EmitLog(ctx, record)
				}
			}
			return
		}

		{
			record := globalevents.LogRecord{
				Category:     globalevents.LogRuntime,
				Level:        slog.LevelInfo,
				EventName:    "cron_async_startup_completed",
				Module:       "app",
				Type:         globalevents.ContentText,
				Content:      fmt.Sprintf("cron async startup completed\nduration=%v", time.Since(startedAt).String()),
				ContentLimit: 4096,
				ResultStatus: globalevents.ResultSucceeded,
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
			_ = globalevents.EmitLog(ctx, record)
		}

	}()
}

func setupCron(ctx context.Context, manager *elcron.Manager, cfg *config.Config) error {
	return maintenance.SetupCron(ctx, manager, cfg)
}

func enabledCronPlatforms(cfg *config.Config) []elcron.PlatformTarget {
	if cfg == nil {
		return nil
	}
	out := []elcron.PlatformTarget{}
	for name, raw := range cfg.Platform {
		if !platformConfigEnabled(raw) {
			continue
		}
		out = append(out, elcron.PlatformTarget{Name: name, SuperadminIDs: cfg.Security.Superadmins[name]})
	}
	return out
}

func platformConfigEnabled(raw map[string]any) bool {
	value, ok := raw["enabled"]
	if !ok {
		return false
	}
	enabled, ok := value.(bool)
	return ok && enabled
}
