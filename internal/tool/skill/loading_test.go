package skill

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"elbot/internal/processenv"
	"elbot/internal/security"
	"elbot/internal/tool/registry"
	"elbot/internal/tool/share"
)

type blockedSkillRegistry struct {
	*registry.Registry
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (r *blockedSkillRegistry) ReplaceSources(sources []share.Source, tools []share.Tool) error {
	r.calls.Add(1)
	close(r.entered)
	<-r.release
	return r.Registry.ReplaceSources(sources, tools)
}

func TestAsyncLoadingSharesScanAndSeparatesWaiterCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		root := t.TempDir()
		writeSkill(t, filepath.Join(root, "agent", "docs"), "---\nname: docs\ndescription: Docs\n---\n# Docs")
		catalog := &blockedSkillRegistry{Registry: registry.NewRegistry(security.DefaultPermission()), entered: make(chan struct{}), release: make(chan struct{})}
		manager := NewManager(root, catalog, nil, processenv.Environment{})
		done := manager.Start(t.Context())
		<-catalog.entered
		results := make(chan error, 4)
		for range 4 {
			go func() { results <- manager.EnsureLoaded(t.Context()) }()
		}
		ctx, cancel := context.WithCancel(t.Context())
		canceled := make(chan error, 1)
		go func() { canceled <- manager.EnsureLoaded(ctx) }()
		synctest.Wait()
		cancel()
		if err := <-canceled; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		synctest.Wait()
		select {
		case err := <-results:
			t.Fatalf("query finished before catalog commit: %v", err)
		default:
		}
		close(catalog.release)
		for range 4 {
			if err := <-results; err != nil {
				t.Fatal(err)
			}
		}
		<-done
		if catalog.calls.Load() != 1 {
			t.Fatalf("scans = %d", catalog.calls.Load())
		}
		if _, ok := catalog.Get("docs"); !ok {
			t.Fatal("loaded skill is missing")
		}
	})
}

func TestAsyncLoadingDoneWaitsForActualExit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		catalog := &blockedSkillRegistry{Registry: registry.NewRegistry(security.DefaultPermission()), entered: make(chan struct{}), release: make(chan struct{})}
		manager := NewManager(t.TempDir(), catalog, nil, processenv.Environment{})
		ctx, cancel := context.WithCancel(t.Context())
		done := manager.Start(ctx)
		<-catalog.entered
		cancel()
		synctest.Wait()
		select {
		case <-done:
			t.Fatal("cancellation reported exit while registry commit is still running")
		default:
		}
		close(catalog.release)
		<-done
	})
}

func TestReloadFailurePreservesLoadedCatalog(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent", "docs")
	writeSkill(t, path, "---\nname: docs\ndescription: Original\n---\n# Docs")
	catalog := registry.NewRegistry(security.DefaultPermission())
	manager := NewManager(root, catalog, nil, processenv.Environment{})
	<-manager.Start(t.Context())
	if err := manager.EnsureLoaded(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(path, "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(path, "SKILL.md"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reload(t.Context()); err == nil {
		t.Fatal("invalid skill reload succeeded")
	}
	if err := manager.EnsureLoaded(t.Context()); err != nil {
		t.Fatalf("last successful catalog became unavailable: %v", err)
	}
	info, ok := catalog.ToolInfo("docs")
	if !ok || info.Description != "Original" {
		t.Fatalf("catalog changed after failed reload: %+v", info)
	}
}
