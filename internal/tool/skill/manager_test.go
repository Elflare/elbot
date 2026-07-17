package skill

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"elbot/internal/tool"
)

func TestManagerSerializesConcurrentReloads(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, filepath.Join(root, "agent", "docs"), "---\nname: docs\ndescription: Docs\n---\n\n# Docs")
	writeElyphSkill(t, filepath.Join(root, "go", "worker"), "#skill worker - Worker\n")
	manager := NewManager(root, tool.NewRegistry())

	const goroutines = 12
	const reloads = 20
	errs := make(chan error, goroutines*reloads)
	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range reloads {
				errs <- manager.Reload(context.Background())
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Reload: %v", err)
		}
	}
	if got := manager.Registry.List(); len(got) != 2 {
		t.Fatalf("registry = %#v, want two skills", got)
	}
	if got := manager.Catalog.List(); len(got) != 2 {
		t.Fatalf("catalog = %#v, want two skills", got)
	}
}
