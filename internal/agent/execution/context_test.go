package execution

import (
	agentshare "elbot/internal/agent/share"

	"context"

	"strings"
	"testing"

	chatroute "elbot/internal/agent/chat"

	"elbot/internal/contextmgr"
	"elbot/internal/llm"

	"elbot/internal/storage"
)

func TestCompactMessagesFiltersToolResultsAndFailedCalls(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	session := &storage.Session{OwnerID: "u", Platform: "cli", PlatformScopeID: "local", Title: "compact"}
	if err := store.Sessions().Create(ctx, session); err != nil {
		t.Fatalf("create session: %v", err)
	}
	for _, record := range []*storage.ToolCallRecord{
		{SessionID: session.ID, ToolCallID: "ok", ToolName: "shell", ActorID: "u", RiskLevel: "low", Success: true},
		{SessionID: session.ID, ToolCallID: "failed", ToolName: "web", ActorID: "u", RiskLevel: "low", Success: false, Error: "boom"},
	} {
		if err := store.ToolCalls().Create(ctx, record); err != nil {
			t.Fatalf("create tool call: %v", err)
		}
	}
	calls := []llm.ToolCallRequest{
		{ID: "ok", Name: "shell", Arguments: `{"command":"go test ./..."}`},
		{ID: "failed", Name: "web", Arguments: `{"q":"failed"}`},
		{ID: "missing", Name: "shell", Arguments: `{"command":"missing"}`},
	}
	toolCall := agentshare.ToolCallStorageMessage(session.ID, "C", "C", calls)
	runtime := &chatroute.Compactor{ToolCalls: store.ToolCalls()}
	messages, err := runtime.CompactMessages(ctx, &contextmgr.LoadedContext{
		Summary: &storage.ContextSummary{Summary: "I"},
		Messages: []storage.Message{
			{Role: storage.RoleUser, Content: "J"},
			toolCall,
			{Role: storage.RoleTool, Content: "secret tool result", ToolCallID: "ok"},
			{Role: storage.RoleAssistant, Content: "H"},
		},
	})
	if err != nil {
		t.Fatalf("compact messages: %v", err)
	}
	if len(messages) != 3 {
		t.Fatalf("messages = %#v", messages)
	}
	if messages[0].Role != storage.RoleUser || messages[0].Content != "I\n\n当前用户输入：\nJ" {
		t.Fatalf("first message = %#v", messages[0])
	}
	if len(messages[1].ToolCalls) != 1 || messages[1].ToolCalls[0].Name != "shell" || !strings.Contains(messages[1].ToolCalls[0].Arguments, "go test") {
		t.Fatalf("tool calls = %#v", messages[1].ToolCalls)
	}
	for _, message := range messages {
		if strings.Contains(message.Content, "secret tool result") {
			t.Fatalf("tool result leaked into compact messages: %#v", messages)
		}
	}
}
