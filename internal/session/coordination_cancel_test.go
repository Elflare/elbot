package session

import (
	"context"
	"elbot/internal/signal"
	"testing"
)

func TestCommittedBindingFactSurvivesCanceledLease(t *testing.T) {
	s := NewService(nil)
	defer s.Close(context.Background())
	scope := Scope{ActorID: "cli:one", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	s.setCurrent(context.Background(), scope, "old")
	old := s.current[scope.Key()]
	count := 0
	c, err := s.changed.Observe(func(ctx context.Context, e BindingChangedEvent) error {
		if ctx.Err() != nil {
			t.Error("committed change inherited cancellation")
		}
		if e.Old == old {
			count++
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	locked, release, err := s.EnterScope(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	s.setCurrent(locked, scope, "new")
	cancel()
	if count != 0 {
		t.Fatal("fact published before releasing scope lock")
	}
	release()
	if old.Valid() || count != 1 {
		t.Fatalf("valid=%t facts=%d", old.Valid(), count)
	}
}
