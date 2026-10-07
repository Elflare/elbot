package app

import (
	"context"
	"errors"
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
				_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
					Category: globalevents.LogRuntime,
					Level:    slog.LevelError,
					Name:     "cron_async_startup_failed",
					Module:   "app",
					Summary:  "cron async startup failed",
					Fields:   []slog.Attr{slog.Any("duration", time.Since(startedAt).String()), slog.Any("error", err.Error())},
				})
			}
			return
		}

		_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
			Category: globalevents.LogRuntime,
			Level:    slog.LevelInfo,
			Name:     "cron_async_startup_completed",
			Module:   "app",
			Summary:  "cron async startup completed",
			Fields:   []slog.Attr{slog.Any("duration", time.Since(startedAt).String())},
		})

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
