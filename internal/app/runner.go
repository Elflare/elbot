package app

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const defaultShutdownTimeout = 30 * time.Second

type cleanupStep struct {
	stopped func() bool
	name    string
	close   func(context.Context) error
}

func (r *Runner) Run(ctx context.Context, opts Options) (runErr error) {
	ctx, cancel := context.WithCancel(ctx)
	var shutdownCtx context.Context
	var shutdownCancel context.CancelFunc
	var bindings *signalBindings
	var logs LogManager
	beginShutdown := func() {
		if logs != nil {
			logs.BeginClose()
		}
		if bindings != nil {
			bindings.BeginClose()
		}
		if shutdownCtx != nil {
			return
		}
		timeout := r.shutdownTimeout
		if timeout <= 0 {
			timeout = defaultShutdownTimeout
		}
		shutdownCtx, shutdownCancel = context.WithTimeout(context.Background(), timeout)
	}
	platformsStopped := true
	var cleanups []cleanupStep
	var stopCron func(context.Context) error
	defer func() {
		runErr = withoutShutdownError(runErr, ctx.Err())
		cancel()
		beginShutdown()
		defer shutdownCancel()
		if stopCron != nil {
			if err := stopCron(shutdownCtx); err != nil {
				if err = withoutShutdownError(err, shutdownCtx.Err()); err != nil {
					runErr = errors.Join(runErr, fmt.Errorf("stop cron: %w", err))
				}
				return
			}
		}
		if !platformsStopped {
			return
		}
		for i := len(cleanups) - 1; i >= 0; i-- {
			if shutdownCtx.Err() != nil {
				break
			}
			err := cleanups[i].close(shutdownCtx)
			if err = withoutShutdownError(err, shutdownCtx.Err()); err != nil {
				runErr = errors.Join(runErr, fmt.Errorf("close %s: %w", cleanups[i].name, err))
			}
			// A callback may still be using every later dependency. Leave those
			// resources to process exit; do not create a background cleanup chain.
			if cleanups[i].stopped != nil && !cleanups[i].stopped() {
				break
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
	if foundation != nil {
		logs = foundation.Logs
		stopCron = foundation.StopCron
	}
	if foundation != nil && foundation.Lifecycle != nil {
		cleanups = append(cleanups, cleanupStep{name: "foundation", close: foundation.Lifecycle.Close})
	}
	if err != nil {
		return err
	}
	if foundation == nil {
		return fmt.Errorf("app: foundation factory returned incomplete components")
	}
	if foundation.Lifecycle == nil {
		return fmt.Errorf("app: foundation factory returned incomplete components")
	}
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
	if runtime != nil {
		bindings = runtime.Signals
		if runtime.Lifecycle != nil {
			step := cleanupStep{name: "runtime", close: runtime.Lifecycle.Close}
			if lifecycle, ok := runtime.Lifecycle.(interface{ stopped() bool }); ok {
				step.stopped = lifecycle.stopped
			}
			cleanups = append(cleanups, step)
		}
		if runtime.Signals != nil {
			cleanups = append(cleanups, cleanupStep{name: "signals", close: runtime.Signals.Close, stopped: runtime.Signals.stopped})
		}
	}
	if err != nil {
		return err
	}
	if runtime == nil {
		return fmt.Errorf("app: runtime factory returned incomplete components")
	}
	if runtime.Lifecycle == nil {
		return fmt.Errorf("app: runtime factory returned incomplete components")
	}
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
	done := make(chan error, 1)
	platformsStopped = false
	go func() {
		done <- r.deps.Executor.Run(ctx, PlatformRunRequest{
			Handler:    runtime.Handler,
			Logger:     foundation.Logger,
			Runtimes:   platforms.Runtimes,
			AfterStart: afterStart,
			Stop:       cancel,
		})
	}()
	select {
	case err := <-done:
		platformsStopped = true
		return err
	case <-ctx.Done():
		beginShutdown()
		select {
		case err := <-done:
			platformsStopped = true
			return err
		case <-shutdownCtx.Done():
			return ctx.Err()
		}
	}
}
