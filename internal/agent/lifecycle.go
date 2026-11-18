package agent

import (
	"context"
)

func (m *Supervisor) BeginClose() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.closed = true
	m.cancel()
	m.sessions.BeginClose()
	m.execution.BeginClose()
	// This observer waits on owned completion channels only; it never releases borrowed dependencies.
	go func() {
		<-m.execution.Done()
		<-m.bridge.Done()
		<-m.sessions.Done()
		// Close starts signal shutdown after the caller has joined external producers.
		<-m.statusChanged.Done()
		close(m.done)
	}()
}

func (m *Supervisor) Done() <-chan struct{} { return m.done }

// Close must follow the caller joining platform and background producers.
// BeginClose alone preserves event sources for their final publications.
func (m *Supervisor) Close(ctx context.Context) error {
	m.BeginClose()
	if err := m.execution.Close(ctx); err != nil {
		return err
	}
	if err := m.sessions.Close(ctx); err != nil {
		return err
	}
	if err := m.bridge.Close(ctx); err != nil {
		return err
	}
	if err := m.statusChanged.Close(ctx); err != nil {
		return err
	}
	select {
	case <-m.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
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
	if err := m.sessions.Start(); err != nil {
		return err
	}
	m.started = true
	return nil
}
