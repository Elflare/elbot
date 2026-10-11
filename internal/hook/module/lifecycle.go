package module

import "context"

// BeginClose prevents activation. Runtime remains available until Dialogue's final expiry callbacks exit.
func (m *Module) BeginClose()                     { m.mu.Lock(); m.closed = true; m.mu.Unlock() }
func (m *Module) Close(ctx context.Context) error { m.BeginClose(); return m.runtime.Close(ctx) }
func (m *Module) Done() <-chan struct{}           { return m.runtime.Done() }
