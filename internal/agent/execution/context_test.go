package execution

import (
	"context"
	"strings"
	"testing"

	chatroute "elbot/internal/agent/chat"
	"elbot/internal/storage"
)

func TestCompactMessagesScopesToolCallResultsBySession(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	session := &storage.Session{ID: storage.NewID()}
	parent := &storage.Session{ID: storage.NewID()}
	for _, row := range []*storage.Session{session, parent} {
		if err := store.Sessions().Create(ctx, row); err != nil {
			t.Fatalf("create session: %v", err)
		}
	}
	for _, record := range []*storage.ToolCallRecord{
		{SessionID: session.ID, ToolCallID: "same", ToolName: "shell", Success: true},
		{SessionID: session.ID, ToolCallID: "missing", ToolName: "web", Success: false},
		{SessionID: parent.ID, ToolCallID: "same", ToolName: "failed-parent", Success: false},
		{SessionID: parent.ID, ToolCallID: "missing", ToolName: "inherited", Success: true},
	} {
		if err := store.ToolCalls().Create(ctx, record); err != nil {
			t.Fatalf("create tool call: %v", err)
		}
	}
	messages := []storage.Message{
		{SessionID: parent.ID, Role: storage.RoleAssistant, Content: "P", ToolCalls: []storage.ToolCall{{ID: "same", Name: "failed-parent"}, {ID: "missing", Name: "inherited"}}},
		{SessionID: session.ID, Role: storage.RoleUser, Content: "J"},
		{SessionID: session.ID, Role: storage.RoleAssistant, Content: "C", SourceContent: "C", ToolCalls: []storage.ToolCall{{ID: "same", Name: "shell", Arguments: `{"command":"safe"}`}, {ID: "missing", Name: "web", Arguments: `{"q":"missing"}`}}},
		{SessionID: session.ID, Role: storage.RoleTool, Content: "secret tool result", ToolCallID: "same"},
		{SessionID: session.ID, Role: storage.RoleAssistant, Content: "H"},
	}
	runtime := &chatroute.Compactor{ToolCalls: store.ToolCalls()}
	compacted, err := runtime.CompactMessages(ctx, messages)
	if err != nil {
		t.Fatalf("compact messages: %v", err)
	}
	if len(compacted) != 4 || len(compacted[0].ToolCalls) != 1 || compacted[0].ToolCalls[0].Name != "inherited" || len(compacted[2].ToolCalls) != 1 || compacted[2].ToolCalls[0].Name != "shell" {
		t.Fatalf("session-scoped calls = %#v", compacted)
	}
	for _, message := range compacted {
		if strings.Contains(message.Content, "secret tool result") {
			t.Fatalf("tool result leaked into compact messages: %#v", compacted)
		}
	}
}
