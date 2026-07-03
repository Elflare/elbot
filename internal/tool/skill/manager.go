package skill

import (
	"context"
	"sync"
	"time"

	"elbot/internal/tool"
)

type Manager struct {
	Root     string
	Catalog  *Catalog
	Scanner  FilesystemScanner
	Registry *tool.Registry

	mu      sync.Mutex
	loaded  bool
	loading bool
	done    chan struct{}
	lastErr error
}

func NewManager(root string, registry *tool.Registry) *Manager {
	scanner := NewFilesystemScanner(root)
	return &Manager{Root: scanner.Root, Catalog: scanner.Catalog, Scanner: scanner, Registry: registry}
}

func (m *Manager) Reload(ctx context.Context) error {
	if m == nil || m.Scanner.Catalog == nil {
		return nil
	}
	err := m.Scanner.Reload(ctx, m.Registry)
	m.mu.Lock()
	m.loaded = err == nil
	m.lastErr = err
	m.mu.Unlock()
	return err
}

func (m *Manager) EnsureLoaded(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	if m.loaded {
		err := m.lastErr
		m.mu.Unlock()
		return err
	}
	if m.loading {
		done := m.done
		m.mu.Unlock()
		select {
		case <-done:
			m.mu.Lock()
			err := m.lastErr
			m.mu.Unlock()
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	m.loading = true
	m.done = make(chan struct{})
	done := m.done
	m.mu.Unlock()

	err := m.Scanner.Reload(ctx, m.Registry)

	m.mu.Lock()
	m.loaded = err == nil
	m.loading = false
	m.lastErr = err
	close(done)
	m.mu.Unlock()
	return err
}

func (m *Manager) StartDelayedReload(ctx context.Context, delay time.Duration) {
	if m == nil {
		return
	}
	go func() {
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return
			}
		}
		_ = m.EnsureLoaded(ctx)
	}()
}

func (m *Manager) Remove(ctx context.Context, name string) error {
	if m == nil {
		return nil
	}
	return m.Scanner.Remove(ctx, m.Registry, name)
}
