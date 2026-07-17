package commands

import (
	"context"
	"sync"
	"testing"

	"elbot/internal/command"
	"elbot/internal/session"
	"elbot/internal/storage"
)

func TestModeCommandsReturnBoundContinuation(t *testing.T) {
	ctx := context.Background()
	store := newCommandTestStore(t)

	svc := session.NewService(store)
	tests := []struct {
		name    string
		scope   session.Scope
		handler func(Deps) command.Handler
		mode    string
	}{
		{name: "work", scope: session.Scope{ActorID: "work", Platform: "cli", PlatformScopeID: "local", IsCLI: true}, handler: NewWork, mode: storage.SessionModeWork},
		{name: "chat", scope: session.Scope{ActorID: "chat", Platform: "cli", PlatformScopeID: "local", IsCLI: true}, handler: NewChat, mode: storage.SessionModeChat},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := Deps{Sessions: svc, Scope: func(context.Context) session.Scope { return tt.scope }}
			result, err := tt.handler(deps).Handle(ctx, command.Request{Args: "hello world"})
			if err != nil {
				t.Fatalf("Handle: %v", err)
			}
			if result.Continuation == nil || result.Continuation.Text != "hello world" {
				t.Fatalf("continuation = %#v", result.Continuation)
			}
			current, err := svc.Current(ctx, tt.scope)
			if err != nil {
				t.Fatalf("Current: %v", err)
			}
			if current.Mode != tt.mode || result.Continuation.SessionID != current.ID {
				t.Fatalf("current = %#v, continuation = %#v", current, result.Continuation)
			}
		})
	}
}

func TestChatCommandDoesNotContinueWhenHistoryBlocksSwitch(t *testing.T) {
	ctx := context.Background()
	store := newCommandTestStore(t)

	svc := session.NewService(store)
	scope := session.Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	work, err := svc.Create(ctx, scope, session.CreateRequest{Title: "work"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.Messages().Append(ctx, &storage.Message{SessionID: work.ID, Role: storage.RoleUser, Content: "history"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	result, err := NewChat(Deps{Sessions: svc, Scope: func(context.Context) session.Scope { return scope }}).Handle(ctx, command.Request{Args: "do not send"})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if result.Continuation != nil {
		t.Fatalf("continuation = %#v", result.Continuation)
	}
}

func TestSessionCommandStateIsScopedAndConcurrent(t *testing.T) {
	state := NewSessionCommandState(10, 30)
	one := session.Scope{ActorID: "one", Platform: "cli", PlatformScopeID: "local"}
	two := session.Scope{ActorID: "two", Platform: "cli", PlatformScopeID: "local"}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			state.set(one, []storage.SessionSummary{{ID: "one-session"}})
			_ = state.get(two)
		}()
		go func() {
			defer wg.Done()
			state.set(two, []storage.SessionSummary{{ID: "two-session"}})
			_ = state.get(one)
		}()
	}
	wg.Wait()

	if got := state.get(one); len(got) != 1 || got[0] != "one-session" {
		t.Fatalf("scope one = %#v", got)
	}
	if got := state.get(two); len(got) != 1 || got[0] != "two-session" {
		t.Fatalf("scope two = %#v", got)
	}
}
