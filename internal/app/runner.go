package app

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const defaultShutdownTimeout = 30 * time.Second

type cleanupStep struct {
	name  string
	close func(context.Context) error
}

func (r *Runner) Run(ctx context.Context, opts Options) (runErr error) {
	var cleanups []cleanupStep
	defer func() {
		shutdownTimeout := r.shutdownTimeout
		if shutdownTimeout <= 0 {
			shutdownTimeout = defaultShutdownTimeout
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		for i := len(cleanups) - 1; i >= 0; i-- {
			if err := cleanups[i].close(shutdownCtx); err != nil {
				runErr = errors.Join(runErr, fmt.Errorf("close %s: %w", cleanups[i].name, err))
			}
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}

	mode, err := r.deps.Environment.ResolveMode(opts.Mode)
	if err != nil {
		return err
	}
	marker, err := r.deps.Environment.ClaimServiceMarker(mode)
	if err != nil {
		return err
	}
	if marker != nil {
		cleanups = append(cleanups, cleanupStep{name: "service marker", close: func(context.Context) error { return marker.Close() }})
	}

	profiler := r.deps.Environment.NewStartupProfiler(opts.StartedAt)
	if profiler == nil {
		return fmt.Errorf("app: environment returned nil startup profiler")
	}
	foundation, err := r.deps.Foundation.Build(ctx, FoundationRequest{Options: opts, Mode: mode, Profiler: profiler})
	if err != nil {
		return err
	}
	if foundation == nil {
		return fmt.Errorf("app: foundation factory returned incomplete components")
	}
	if foundation.Lifecycle == nil {
		return fmt.Errorf("app: foundation factory returned incomplete components")
	}
	cleanups = append(cleanups, cleanupStep{name: "foundation", close: foundation.Lifecycle.Close})
	if foundation.Logger == nil {
		return fmt.Errorf("app: foundation factory returned incomplete components")
	}

	models, err := r.deps.Models.Build(ModelRequest{Foundation: foundation, Profiler: profiler})
	if err != nil {
		return err
	}
	platforms, err := r.deps.Platforms.Build(PlatformRequest{Foundation: foundation, Mode: mode, Profiler: profiler})
	if err != nil {
		return err
	}
	runtime, err := r.deps.Runtime.Build(ctx, RuntimeRequest{Foundation: foundation, Models: models, Platforms: platforms, Profiler: profiler})
	if err != nil {
		return err
	}
	if runtime == nil {
		return fmt.Errorf("app: runtime factory returned incomplete components")
	}
	if runtime.Lifecycle == nil {
		return fmt.Errorf("app: runtime factory returned incomplete components")
	}
	cleanups = append(cleanups, cleanupStep{name: "runtime", close: runtime.Lifecycle.Close})
	if runtime.Handler == nil {
		return fmt.Errorf("app: runtime factory returned incomplete components")
	}

	platforms, err = r.deps.Integrations.Attach(ctx, IntegrationRequest{
		Foundation: foundation,
		Runtime:    runtime,
		Platforms:  platforms,
		Mode:       mode,
		Profiler:   profiler,
	})
	if err != nil {
		return err
	}

	startupDuration := profiler.Flush()
	foundation.Logger.Info("elbot startup completed", "startup_duration", startupDuration.String())
	var afterStart func(context.Context)
	if shouldStartCron(mode) && foundation.StartCron != nil {
		afterStart = func(ctx context.Context) {
			foundation.StartCron(ctx, runtime.CronService)
		}
	}
	return r.deps.Executor.Run(ctx, PlatformRunRequest{
		Handler:    runtime.Handler,
		Logger:     foundation.Logger,
		Runtimes:   platforms.Runtimes,
		AfterStart: afterStart,
	})
}
