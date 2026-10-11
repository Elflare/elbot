package module

import "context"

func (m *Module) BeginClose() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.closed = true
	if m.notifications != nil {
		m.notifications.BeginClose()
	}
	m.cancel()
	if !m.started {
		go func() {
			if m.notifications != nil {
				_ = m.notifications.Close(context.Background())
			}
			close(m.done)
		}()
	}
}
func (m *Module) Done() <-chan struct{} { return m.done }
func (m *Module) Close(ctx context.Context) error {
	m.BeginClose()
	if m.notifications != nil {
		if err := m.notifications.Close(ctx); err != nil {
			return err
		}
	}
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
