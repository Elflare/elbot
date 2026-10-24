// 协调 Hook 的启动、停止接入、关闭和实际退出。

package hook

import "context"

func (m *Supervisor) Start() error {
	m.startMu.Lock()
	defer m.startMu.Unlock()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return context.Canceled
	}
	if err := m.parent.Err(); err != nil {
		m.mu.Unlock()
		return err
	}
	if m.started {
		m.mu.Unlock()
		return nil
	}
	m.calls.Add(1)
	m.mu.Unlock()
	defer m.calls.Done()

	// The loader reports configuration failures at their source. Initial load
	// remains non-fatal; management commands can reload a corrected configuration.
	_, _ = m.control.HookReload()
	if err := m.startPlatformEvents(m.parent); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return context.Canceled
	}
	m.started = true
	return nil
}

// BeginClose stops new connections and platform work. Agent callbacks and the
// plugin runtime remain usable while App joins the upstream producers.
func (m *Supervisor) BeginClose() {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	m.beginClosePlatformEvents()
}

func (m *Supervisor) Done() <-chan struct{} { return m.done }

// Close follows App's Agent shutdown. A timeout only stops the caller waiting;
// done continues to represent actual exit, including callbacks that ignore cancellation.
func (m *Supervisor) Close(ctx context.Context) error {
	m.BeginClose()
	m.closeOnce.Do(func() {
		m.mu.Lock()
		m.closing = true
		disconnect := m.disconnect
		m.disconnect = nil
		m.cancel()
		m.mu.Unlock()
		if disconnect != nil {
			disconnect()
		}
		go func() {
			m.calls.Wait()
			<-m.platformEventsDone()
			// Runtime owns worker cancellation and media cleanup. Preserve the
			// caller's cleanup budget, then wait for the actual resource exit.
			_ = m.runtime.Close(ctx)
			<-m.runtime.Done()
			m.closeErr = m.runtime.Close(context.Background())
			close(m.done)
		}()
	})
	select {
	case <-m.done:
		return m.closeErr
	default:
	}
	select {
	case <-m.done:
		return m.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// beginCall owns admission at the external boundary, not individual handler
// execution. Close cancels admitted contexts and joins them before closing children.
func (m *Supervisor) beginCall(ctx context.Context) (context.Context, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing {
		return nil, nil, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	m.calls.Add(1)
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(m.ctx, cancel)
	return ctx, func() {
		stop()
		cancel()
		m.calls.Done()
	}, nil
}
