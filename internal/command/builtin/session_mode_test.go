package builtin

import (
	"context"
	"elbot/internal/contextinfo"
	"errors"
	"testing"

	command "elbot/internal/command/share"
	"elbot/internal/session"
	"elbot/internal/storage"
)

func TestModeCommandsReturnBoundContinuation(t *testing.T) {
	ctx := context.Background()
	store := newCommandTestStore(t)

	svc := newSessionSupervisor(t, store)
	tests := []struct {
		name    string
		scope   session.Scope
		handler func(Deps) command.Handler
		mode    string
	}{
		{name: "work", scope: session.Scope{Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "work"}, Platform: "cli", ChatID: "local", IsCLI: true, ChatType: "private"}, handler: NewWork, mode: storage.SessionModeWork},
		{name: "chat", scope: session.Scope{Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "chat"}, Platform: "cli", ChatID: "local", IsCLI: true, ChatType: "private"}, handler: NewChat, mode: storage.SessionModeChat},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := Deps{CommandText: testCommandText, Sessions: svc, Scope: func(context.Context) session.Scope { return tt.scope }}
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

	svc := newSessionSupervisor(t, store)
	scope := session.Scope{Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "u1"}, Platform: "cli", ChatID: "local", IsCLI: true, ChatType: "private"}
	work, err := svc.Create(ctx, scope, session.CreateRequest{Title: "work"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.Messages().Append(ctx, &storage.Message{ID: storage.NewID(), SessionID: work.ID, Role: storage.RoleUser, Content: "history"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	result, err := NewChat(Deps{CommandText: testCommandText, Sessions: svc, Scope: func(context.Context) session.Scope { return scope }}).Handle(ctx, command.Request{Args: "do not send"})
	if result != nil || !errors.Is(err, session.ErrChatModeRequiresEmptySession) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
