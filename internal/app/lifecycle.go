package app

import (
	"context"
	"errors"
	"fmt"

	hookruntime "elbot/internal/hook/runtime"
)

type runtimeLifecycle struct {
	cancel    context.CancelFunc
	skillDone <-chan struct{}
	hooks     *hookruntime.Manager
}

func (l *runtimeLifecycle) Close(ctx context.Context) error {
	l.cancel()
	var errs []error
	if l.hooks != nil {
		if err := l.hooks.Close(ctx); err != nil {
			errs = append(errs, fmt.Errorf("close hook runtime: %w", err))
		}
	}
	if l.skillDone != nil {
		select {
		case <-l.skillDone:
		case <-ctx.Done():
			errs = append(errs, fmt.Errorf("wait skill loading: %w", ctx.Err()))
		}
	}
	return errors.Join(errs...)
}

func (l *runtimeLifecycle) stopped() bool {
	if l.hooks != nil {
		select {
		case <-l.hooks.Done():
		default:
			return false
		}
	}
	if l.skillDone != nil {
		select {
		case <-l.skillDone:
		default:
			return false
		}
	}
	return true
}
