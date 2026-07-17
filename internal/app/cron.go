package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"elbot/internal/config"
	elcron "elbot/internal/cron"
	"elbot/internal/maintenance"
)

func startCronAsync(ctx context.Context, manager *elcron.Manager, service *elcron.Service, cfg *config.Config, logger *slog.Logger, done chan<- struct{}) {
	go func() {
		defer close(done)
		startedAt := time.Now()
		if err := setupCron(ctx, manager, cfg); err != nil {
			if logger != nil && !errors.Is(err, context.Canceled) {
				logger.Warn("cron async startup failed", "duration", time.Since(startedAt).String(), "error", err.Error())
			}
			return
		}
		if logger != nil {
			logger.Info("cron async startup completed", "duration", time.Since(startedAt).String())
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
