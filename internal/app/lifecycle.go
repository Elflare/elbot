package app

import (
	"context"
	"errors"
	"fmt"

	elcron "elbot/internal/cron"
	hookruntime "elbot/internal/hook/runtime"
	"elbot/internal/modelmgr"
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
	models    *modelmgr.Service
	cron      *elcron.Service
}

// BeginClose stops consumer admission before waiting for producers or queues.
func (l *runtimeLifecycle) BeginClose() {
	l.cancel()
	if l.cron != nil {
		l.cron.BeginClose()
	}
	if agent, ok := l.agent.(interface{ BeginClose() }); ok {
		agent.BeginClose()
	}
}

func (l *runtimeLifecycle) Close(ctx context.Context) error {
	l.BeginClose()
	var errs []error
	// Recovery can still be using Agent, Hook, models and storage.
	if l.cron != nil {
		if err := l.cron.Close(ctx); err != nil {
			errs = append(errs, fmt.Errorf("close cron connection recovery: %w", err))
		}
		select {
		case <-l.cron.Done():
		default:
			return errors.Join(errs...)
		}
	}
	if l.agent != nil {
		if err := l.agent.Close(ctx); err != nil {
			errs = append(errs, fmt.Errorf("close agent workers: %w", err))
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
	if l.models != nil {
		errs = append(errs, l.models.Close())
	}
	return errors.Join(errs...)
}

func (l *runtimeLifecycle) stopped() bool {
	if l.cron != nil {
		select {
		case <-l.cron.Done():
		default:
			return false
		}
	}
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
