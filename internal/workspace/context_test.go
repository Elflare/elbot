package workspace

import (
	"context"
	"errors"
	"testing"
)

func TestWithoutPathScopePreservesSessionAndCancellation(t *testing.T) {
	base, cancel := context.WithCancel(t.Context())
	defer cancel()
	store := &fallbackWorkspaceStore{dir: t.TempDir()}
	scope := PathScope{Dir: t.TempDir(), Restricted: true}
	parent := WithPathScope(WithWorkspaceStore(base, store), scope)
	child := WithoutPathScope(parent)
	if _, ok := PathScopeFromContext(child); ok {
		t.Fatal("removed scope is still visible")
	}
	if got, ok := PathScopeFromContext(parent); !ok || got != scope {
		t.Fatalf("parent scope changed: %+v %t", got, ok)
	}
	if got, ok := WorkspaceStoreFromContext(child); !ok || got != store {
		t.Fatal("removing scope lost session store")
	}
	if got, err := CurrentWorkspaceDir(child); err != nil || got != store.dir {
		t.Fatalf("session directory not restored: %q %v", got, err)
	}
	cancel()
	if !errors.Is(child.Err(), context.Canceled) {
		t.Fatalf("cancellation was lost: %v", child.Err())
	}
}
