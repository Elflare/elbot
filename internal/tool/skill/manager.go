package skill

import (
	"context"

	"elbot/internal/tool"
)

type Manager struct {
	Root     string
	Catalog  *Catalog
	Scanner  FilesystemScanner
	Registry *tool.Registry
}

func NewManager(root string, registry *tool.Registry) *Manager {
	scanner := NewFilesystemScanner(root)
	return &Manager{Root: scanner.Root, Catalog: scanner.Catalog, Scanner: scanner, Registry: registry}
}

func (m *Manager) Reload(ctx context.Context) error {
	if m == nil || m.Scanner.Catalog == nil {
		return nil
	}
	return m.Scanner.Reload(ctx, m.Registry)
}

func (m *Manager) Remove(ctx context.Context, name string) error {
	if m == nil {
		return nil
	}
	return m.Scanner.Remove(ctx, m.Registry, name)
}
