package module

import "context"

func (m *Module) BeginClose() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.closed = true
	m.cancel()
	if !m.started {
		close(m.done)
	}
}
func (m *Module) Done() <-chan struct{} { return m.done }
func (m *Module) Close(ctx context.Context) error {
	m.BeginClose()
	select {
	case <-m.done:
		return nil
	default:
	}
	select {
	case <-m.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
