package skill

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"elbot/internal/tool"
	"elbot/internal/utils/fileops"
)

type Manager struct {
	Root     string
	Catalog  *Catalog
	Scanner  FilesystemScanner
	Registry *tool.Registry

	reloadMu sync.Mutex
	mu       sync.Mutex
	loaded   bool
	loading  bool
	done     chan struct{}
	lastErr  error
}

func NewManager(root string, registry *tool.Registry) *Manager {
	scanner := NewFilesystemScanner(root)
	return &Manager{Root: scanner.Root, Catalog: scanner.Catalog, Scanner: scanner, Registry: registry}
}

func (m *Manager) Reload(ctx context.Context) error {
	if m == nil || m.Scanner.Catalog == nil {
		return nil
	}
	m.reloadMu.Lock()
	err := m.Scanner.Reload(ctx, m.Registry)
	m.setReloadResult(err)
	m.reloadMu.Unlock()
	return err
}

func (m *Manager) setReloadResult(err error) {
	m.mu.Lock()
	m.loaded = err == nil
	m.lastErr = err
	m.mu.Unlock()
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

	m.reloadMu.Lock()
	err := m.Scanner.Reload(ctx, m.Registry)

	m.mu.Lock()
	m.loaded = err == nil
	m.loading = false
	m.lastErr = err
	close(done)
	m.mu.Unlock()
	m.reloadMu.Unlock()
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
	m.reloadMu.Lock()
	err := m.Scanner.Remove(ctx, m.Registry, name)
	m.setReloadResult(err)
	m.reloadMu.Unlock()
	return err
}

// WriteAgentSkillConfig commits an AgentSkill manifest and rolls the file back
// if the complete skill snapshot cannot be reloaded.
func (m *Manager) WriteAgentSkillConfig(ctx context.Context, name, content string) error {
	if m == nil || m.Catalog == nil || m.Registry == nil {
		return fmt.Errorf("skill manager is not configured")
	}
	name = strings.TrimSpace(name)
	if err := validateSkillName(name); err != nil {
		return err
	}
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("toml is required for write")
	}
	if _, err := ParseAgentSkillManifest([]byte(content)); err != nil {
		return err
	}

	m.reloadMu.Lock()
	defer m.reloadMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	record, ok := m.Catalog.Get(name)
	if !ok || record.Kind != KindAgent {
		return fmt.Errorf("AgentSkill %q not found", name)
	}
	path := AgentSkillConfigPath(record.Root)
	previous, err := captureFile(path)
	if err != nil {
		return err
	}
	if err := fileops.AtomicWriteFile(path, []byte(content), existingFileMode(path)); err != nil {
		return err
	}
	if err := m.Scanner.Reload(ctx, m.Registry); err != nil {
		if rollbackErr := previous.restore(path); rollbackErr != nil {
			combined := fmt.Errorf("reload and restore previous %s failed: %w", AgentSkillConfigFile, errors.Join(err, rollbackErr))
			m.setReloadResult(combined)
			return combined
		}
		return fmt.Errorf("reload failed; restored previous %s: %w", AgentSkillConfigFile, err)
	}
	m.setReloadResult(nil)
	return nil
}

type fileSnapshot struct {
	exists bool
	data   []byte
	mode   os.FileMode
}

func captureFile(path string) (fileSnapshot, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return fileSnapshot{}, nil
	}
	if err != nil {
		return fileSnapshot{}, fmt.Errorf("read existing %s: %w", AgentSkillConfigFile, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fileSnapshot{}, fmt.Errorf("stat existing %s: %w", AgentSkillConfigFile, err)
	}
	return fileSnapshot{exists: true, data: data, mode: info.Mode().Perm()}, nil
}

func (s fileSnapshot) restore(path string) error {
	if s.exists {
		return fileops.AtomicWriteFile(path, s.data, s.mode)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
