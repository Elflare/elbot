package agent

import (
	"context"
	"errors"
)

func (m *Supervisor) BeginClose() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.closed = true
	m.cancel()
	if m.execution != nil {
		m.execution.BeginClose()
	}
	if m.bridge != nil {
		m.bridge.BeginClose()
	}
	m.bindings.BeginClose()
	// This observer waits on owned completion channels only; it never releases borrowed dependencies.
	go func() {
		if m.execution != nil {
			<-m.execution.Done()
		}
		if m.bridge != nil {
			<-m.bridge.Done()
		}
		if m.sessions != nil {
			<-m.sessions.Done()
		}
		for _, connection := range m.bindings.connections {
			<-connection.Done()
		}
		close(m.done)
	}()
}

func (m *Supervisor) Done() <-chan struct{} { return m.done }

func (m *Supervisor) Close(ctx context.Context) error {
	m.BeginClose()
	var errs []error
	// Disconnect producers before waiting for final expiry output and session naming.
	errs = append(errs, m.bindings.Close(ctx))
	if !m.bindings.stopped() {
		return errors.Join(errs...)
	}
	if m.execution != nil {
		errs = append(errs, m.execution.Close(ctx))
		select {
		case <-m.execution.Done():
		default:
			return errors.Join(errs...)
		}
	}
	if m.bridge != nil {
		errs = append(errs, m.bridge.Close(ctx))
		select {
		case <-m.bridge.Done():
		default:
			return errors.Join(errs...)
		}
	}
	if m.sessions != nil {
		errs = append(errs, m.sessions.Close(ctx))
	}
	select {
	case <-m.done:
	default:
		select {
		case <-m.done:
		case <-ctx.Done():
			errs = append(errs, ctx.Err())
		}
	}
	return errors.Join(errs...)
}

func (m *Supervisor) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return context.Canceled
	}
	if m.started {
		return nil
	}
	m.started = true
	m.sessions.StartNaming(m.ctx)
	return m.bridge.StartPlatformEvents(m.ctx)
}
