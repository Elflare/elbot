package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"elbot/internal/contextinfo"
	globalevents "elbot/internal/events"
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
	var logs LogManager
	var beginWiringClose func()
	beginShutdown := func() {
		if beginWiringClose != nil {
			beginWiringClose()
		}
		if logs != nil {
			logs.BeginClose()
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
	var cleanups []cleanupStep
	defer func() {
		runErr = withoutShutdownError(runErr, ctx.Err())
		cancel()
		beginShutdown()
		defer shutdownCancel()
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

	models, err := r.deps.Models.Build(ModelRequest{Foundation: foundation, Profiler: profiler})
	if err != nil {
		return err
	}
	wiring, err := r.deps.Wiring.Build(ctx, WiringRequest{Foundation: foundation, Models: models, Platforms: r.deps.Platforms, Mode: mode, Profiler: profiler})
	if wiring != nil && wiring.Lifecycle != nil {
		beginWiringClose = wiring.Lifecycle.BeginClose
		cleanups = append(cleanups, cleanupStep{name: "wiring", close: wiring.Lifecycle.Close, stopped: func() bool {
			select {
			case <-wiring.Lifecycle.Done():
				return true
			default:
				return false
			}
		}})
	}
	if err != nil {
		return err
	}
	if wiring == nil {
		return fmt.Errorf("app: wiring factory returned incomplete components")
	}
	if wiring.Lifecycle == nil {
		return fmt.Errorf("app: wiring factory returned incomplete components")
	}
	if err := wiring.Start(cancel); err != nil {
		return err
	}
	startupDuration := profiler.Flush()
	{
		record := globalevents.LogRecord{
			Category:     globalevents.LogRuntime,
			Level:        slog.LevelInfo,
			EventName:    "elbot_startup_completed",
			Module:       "app",
			Type:         globalevents.ContentText,
			Content:      fmt.Sprintf("elbot startup completed\nstartup_duration=%v", startupDuration.String()),
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
	if err := wiring.Platform.Wait(ctx); err != nil {
		return err
	}
	return wiring.Background.Wait(ctx)
}

// withoutShutdownError removes expected cancellation leaves, not entire joined
// errors. In particular, a failed startup must survive a simultaneous shutdown.
func withoutShutdownError(err, expected error) error {
	if err == nil || expected == nil {
		return err
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var kept []error
		for _, child := range joined.Unwrap() {
			kept = append(kept, withoutShutdownError(child, expected))
		}
		return errors.Join(kept...)
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok && wrapped.Unwrap() != nil {
		// Keep the original wrapper when it contains no expected cancellation.
		if !errors.Is(err, expected) {
			return err
		}
		return withoutShutdownError(wrapped.Unwrap(), expected)
	}
	if errors.Is(err, expected) {
		return nil
	}
	return err
}
