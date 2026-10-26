// lifecycle.go 保留旧平台运行与退出管理。
// TODO：按职责迁入根部 lifecycle.go并更新调用方后删除本文件。

package module

import "context"

func (m *Module) BeginClose() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.closed = true
	if m.ui != nil {
		m.ui.BeginClose()
	}
	if m.notifications != nil {
		m.notifications.BeginClose()
	}
	m.cancel()
	if !m.started {
		go func() {
			if m.ui != nil {
				_ = m.ui.Close(context.Background())
			}
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
	if m.ui != nil {
		if err := m.ui.Close(ctx); err != nil {
			return err
		}
	}
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
