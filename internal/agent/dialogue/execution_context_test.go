package dialogue

import (
	"context"
	agentshare "elbot/internal/agent/share"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

func TestForegroundRefreshUsesInjectedRouteAndKeepsRequestLifetime(t *testing.T) {
	type routeKey struct{}
	request, cancel := context.WithCancel(t.Context())
	defer cancel()
	request = context.WithValue(request, routeKey{}, "background")
	calls := 0
	request = agentshare.WithMessageContext(request, agentshare.MessageContext{
		RestoreMessageContext: func(ctx, source context.Context) context.Context {
			calls++
			return context.WithValue(ctx, routeKey{}, source.Value(routeKey{}))
		},
	})
	source, stopSource := context.WithCancel(context.Background())
	source = context.WithValue(source, routeKey{}, "foreground")
	source = agentshare.WithMessageContext(source, agentshare.MessageContext{RawText: "new input", Segments: []agentshare.MessageSegment{{Text: "new input"}}})
	e := turn.NewExecution("run")
	e.Adopt(source, storage.Session{})
	stopSource()
	refreshed := (ExecutionView{}).Context(turn.WithExecution(request, e))
	msg, ok := agentshare.MessageContextFrom(refreshed)
	if calls != 1 || refreshed.Value(routeKey{}) != "foreground" || !ok || msg.RawText != "new input" || refreshed.Err() != nil {
		t.Fatalf("restore=%d route=%v input=%+v err=%v", calls, refreshed.Value(routeKey{}), msg, refreshed.Err())
	}
	cancel()
	if refreshed.Err() != context.Canceled {
		t.Fatal("request cancellation lost")
	}
}

func TestForegroundRefreshClearsAbsentSourceAndActor(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ctx = agentshare.WithMessageContext(contextinfo.WithConversation(ctx, contextinfo.Conversation{Source: contextinfo.Source{Platform: "background"}, PlatformData: "old-connection"}), agentshare.MessageContext{

		BufferAssistantOutput: true,
	})
	ctx = contextinfo.WithActor(ctx, contextinfo.Actor{ID: "old", Role: contextinfo.RoleSuperadmin})
	ctx = contextinfo.WithModel(ctx, contextinfo.Model{Provider: "fixed", Model: "m", APIType: "chat"})
	e := turn.NewExecution("run")
	e.Adopt(context.Background(), storage.Session{})
	ctx = turn.WithExecution(ctx, e)
	cancel()
	refreshed := (ExecutionView{}).Context(ctx)
	if _, ok := contextinfo.ActorFromContext(refreshed); ok {
		t.Fatal("foreground without actor inherited background authorization")
	}
	if _, ok := contextinfo.ConversationFromContext(refreshed); ok {
		t.Fatal("foreground without conversation inherited a routing snapshot")
	}
	if _, ok := agentshare.MessageContextFrom(refreshed); ok {
		t.Fatal("foreground without platform context retained platform state")
	}
	if model, ok := contextinfo.ModelFromContext(refreshed); !ok || model.Provider != "fixed" {
		t.Fatal("source refresh replaced the fixed model")
	}
	if refreshed.Err() != context.Canceled {
		t.Fatal("foreground refresh lost the request cancellation")
	}
}
