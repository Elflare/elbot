package dialogue

import (
	"context"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/platform"
	"elbot/internal/turn"
)

func TestForegroundRefreshClearsAbsentSourceAndActor(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ctx = platform.WithMessageContext(ctx, platform.MessageContext{
		Conversation:          contextinfo.Conversation{Source: contextinfo.Source{Platform: "background"}, PlatformData: "old-connection"},
		BufferAssistantOutput: true,
	})
	ctx = contextinfo.WithActor(ctx, contextinfo.Actor{ID: "old", Role: contextinfo.RoleSuperadmin})
	ctx = contextinfo.WithModel(ctx, contextinfo.Model{Provider: "fixed", Model: "m", APIType: "chat"})
	e := turn.NewExecution("run")
	e.Adopt(context.Background())
	ctx = turn.WithExecution(ctx, e)
	cancel()
	refreshed := (ExecutionView{}).Context(ctx)
	if _, ok := contextinfo.ActorFromContext(refreshed); ok {
		t.Fatal("foreground without actor inherited background authorization")
	}
	if _, ok := contextinfo.ConversationFromContext(refreshed); ok {
		t.Fatal("foreground without conversation inherited a routing snapshot")
	}
	if _, ok := platform.MessageContextFrom(refreshed); ok {
		t.Fatal("foreground without platform context retained platform state")
	}
	if model, ok := contextinfo.ModelFromContext(refreshed); !ok || model.Provider != "fixed" {
		t.Fatal("source refresh replaced the fixed model")
	}
	if refreshed.Err() != context.Canceled {
		t.Fatal("foreground refresh lost the request cancellation")
	}
}
