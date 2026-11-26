package storage

import (
	"context"
	"testing"

	"elbot/internal/contextinfo"
)

func TestToolCallRepositoryUsageBySession(t *testing.T) {
	store := NewStoreForTest(t)
	ctx := context.Background()

	session := &Session{ID: NewID(), Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "u1"}, Platform: "cli", ChatID: "local", Title: "tools", ChatType: "private"}
	if err := store.CreateSession(ctx, session); err != nil {
		t.Fatalf("create session: %v", err)
	}
	other := &Session{ID: NewID()}
	if err := store.CreateSession(ctx, other); err != nil {
		t.Fatalf("create other session: %v", err)
	}
	for _, record := range []*ToolCallRecord{
		{SessionID: session.ID, ToolCallID: "call_1", ToolName: "shell", Actor: contextinfo.ActorIdentity{Platform: "cli", ID: "u1"}, RiskLevel: "low", Success: true},
		{SessionID: session.ID, ToolCallID: "call_2", ToolName: "shell", Actor: contextinfo.ActorIdentity{Platform: "cli", ID: "u1"}, RiskLevel: "low", Success: false, Error: "boom"},
		{SessionID: session.ID, ToolCallID: "call_3", ToolName: "web_search", Actor: contextinfo.ActorIdentity{Platform: "cli", ID: "u1"}, RiskLevel: "low", Success: true},
		{SessionID: other.ID, ToolCallID: "call_2", ToolName: "shell", Actor: contextinfo.ActorIdentity{Platform: "cli", ID: "u1"}, RiskLevel: "low", Success: true},
	} {
		if err := store.CreateToolCallRecord(ctx, record); err != nil {
			t.Fatalf("create tool call: %v", err)
		}
	}

	usage, err := store.UsageBySession(ctx, session.ID)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if len(usage) != 2 || usage[0].ToolName != "shell" || usage[0].Count != 2 || usage[1].ToolName != "web_search" || usage[1].Count != 1 {
		t.Fatalf("usage = %#v", usage)
	}

	successful, err := store.SuccessfulIDs(ctx, session.ID, []string{"call_1", "call_2", "call_3", "missing", "call_1"})
	if err != nil {
		t.Fatalf("successful ids: %v", err)
	}
	if len(successful) != 2 || !successful["call_1"] || !successful["call_3"] || successful["call_2"] {
		t.Fatalf("successful ids = %#v", successful)
	}
	otherSuccessful, err := store.SuccessfulIDs(ctx, other.ID, []string{"call_2"})
	if err != nil || len(otherSuccessful) != 1 || !otherSuccessful["call_2"] {
		t.Fatalf("other session successful ids = %#v, %v", otherSuccessful, err)
	}
}
