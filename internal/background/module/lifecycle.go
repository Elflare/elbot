package module

import (
	"context"
	"errors"
)

func (m *Module) BeginClose() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.closed = true
	m.cancel()
	if m.cron != nil {
		m.cron.BeginClose()
	}
	var stopped context.Context
	if m.cronManager != nil {
		stopped = m.cronManager.Stop()
	}
	go func() {
		if m.startupDone != nil {
			<-m.startupDone
		}
		if stopped != nil {
			<-stopped.Done()
		}
		if m.elnisDone != nil {
			<-m.elnisDone
		}
		if m.cron != nil {
			<-m.cron.Done()
		}
		close(m.done)
	}()
}

func (m *Module) Done() <-chan struct{} { return m.done }

func (m *Module) Close(ctx context.Context) error {
	m.BeginClose()
	var errs []error
	if m.cron != nil {
		errs = append(errs, m.cron.Close(ctx))
	}
	select {
	case <-m.done:
	default:
		select {
		case <-m.done:
		case <-ctx.Done():
			return errors.Join(append(errs, ctx.Err())...)
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.releaseTools != nil {
		m.releaseTools()
		m.releaseTools = nil
	}
	return errors.Join(errs...)
}
