package app

import (
	"context"
	"errors"
	"fmt"

	hookruntime "elbot/internal/hook/runtime"
	"elbot/internal/session"
)

type runtimeLifecycle struct {
	cancel context.CancelFunc
	agent  interface {
		Close(context.Context) error
		Done() <-chan struct{}
	}
	skillDone <-chan struct{}
	hooks     *hookruntime.Manager
	sessions  *session.Service
}

func (l *runtimeLifecycle) Close(ctx context.Context) error {
	l.cancel()
	var errs []error
	if l.agent != nil {
		if err := l.agent.Close(ctx); err != nil {
			errs = append(errs, fmt.Errorf("close append confirmation waits: %w", err))
		}
		select {
		case <-l.agent.Done():
		default:
			// Expiry output may still be using Hooks, storage or senders.
			return errors.Join(errs...)
		}
	}
	if l.sessions != nil {
		if err := l.sessions.Close(ctx); err != nil {
			errs = append(errs, fmt.Errorf("close session naming: %w", err))
		}
	}
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
	if l.agent != nil {
		select {
		case <-l.agent.Done():
		default:
			return false
		}
	}
	if l.sessions != nil {
		select {
		case <-l.sessions.Done():
		default:
			return false
		}
	}
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
