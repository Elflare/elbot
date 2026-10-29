package share_test

import (
	"context"
	"sync"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/session/share"
)

func TestBindingIdentityAndPermanentInvalidation(t *testing.T) {
	scope := share.Scope{ActorID: "u", Platform: "p", PlatformScopeID: "s"}
	b := share.NewBinding(scope, "session", 42)
	// Session 根部使用独立定义类型和指针转换，不复制原子字段。
	type publicBinding share.Binding
	public := (*publicBinding)(b)
	if (*share.Binding)(public) != b || b.Scope() != scope || b.SessionID() != "session" || b.Version() != 42 {
		t.Fatal("binding identity or immutable facts changed")
	}
	if !b.Valid() || (*share.Binding)(nil).Valid() || new(share.Binding).Valid() {
		t.Fatal("unexpected initial validity")
	}
	var readers sync.WaitGroup
	for range 8 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			invalid := false
			for range 1000 {
				valid := b.Valid()
				if invalid && valid {
					t.Error("invalid binding became valid")
				}
				invalid = !valid
				if b.Scope() != scope || b.SessionID() != "session" || b.Version() != 42 {
					t.Error("invalidation changed immutable facts")
				}
			}
		}()
	}
	b.Invalidate()
	b.Invalidate()
	readers.Wait()
	next := share.NewBinding(scope, "session", 43)
	if b.Valid() || next == b || !next.Valid() || (*share.Binding)(public).Valid() {
		t.Fatal("new activation revived or reused the original binding")
	}
}

func TestBindingContextPreservesIdentityAndExplicitRemoval(t *testing.T) {
	facts := contextinfo.Execution{SessionID: "public-fact", RunID: "run"}
	base := contextinfo.WithExecution(context.Background(), facts)
	b := share.NewBinding(share.Scope{}, "internal-id", 1)
	ctx := share.WithBinding(base, b)
	b.Invalidate()
	if got, ok := share.BindingFromContext(ctx); got != b || !ok {
		t.Fatal("context lost original invalid binding")
	}
	cleared := share.WithBinding(ctx, nil)
	for _, ctx := range []context.Context{base, cleared} {
		if got, ok := share.BindingFromContext(ctx); got != nil || ok {
			t.Fatal("absent binding or explicit removal returned a binding")
		}
	}
	if got, _ := share.BindingFromContext(ctx); got != b {
		t.Fatal("removal mutated parent context")
	}
	if got, _ := contextinfo.ExecutionFromContext(cleared); got != facts {
		t.Fatal("internal binding context rewrote public execution facts")
	}
}
