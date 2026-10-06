package session

import (
	"fmt"
	"testing"

	"elbot/internal/storage"
)

func sessionTestToolPair(sessionID, suffix string) *storage.ToolPair {
	headID, resultID := "head-"+suffix, "result-"+suffix
	return &storage.ToolPair{
		Call: &storage.Message{ID: headID, SessionID: sessionID, Role: storage.RoleAssistant,
			Metadata: fmt.Sprintf(`{"tool_calls":[{"ID":%q,"Name":"tool","Arguments":"{}"}],"tool_result_message_id":%q}`, suffix, resultID)},
		Result: &storage.Message{ID: resultID, SessionID: sessionID, Role: storage.RoleTool, ToolCallID: suffix, Content: suffix},
	}
}

func TestToolForkBoundaryIncludesResultAndExcludesLaterPairs(t *testing.T) {
	svc, store := newTestService(t)
	ctx := t.Context()
	scope := Scope{ActorID: "cli:local", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	source, err := svc.Create(ctx, scope, CreateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	first := sessionTestToolPair(source.ID, "first")
	for _, pair := range []*storage.ToolPair{first, sessionTestToolPair(source.ID, "later")} {
		if err := store.Dialogues().Commit(ctx, storage.DialogueCommit{SessionID: source.ID, ToolPair: pair}); err != nil {
			t.Fatal(err)
		}
	}
	fork, err := svc.Fork(ctx, scope, first.Call.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fork.ForkFromMessageID != first.Result.ID {
		t.Fatalf("fork=%+v", fork)
	}
	rows, err := store.Messages().ListBySessionUpTo(ctx, fork.ParentSessionID, fork.ForkFromMessageID)
	if err != nil || len(rows) != 2 || rows[1].ID != first.Result.ID {
		t.Fatalf("boundary=%+v %v", rows, err)
	}
}

func TestBackgroundCopyRemapsToolPairLinks(t *testing.T) {
	svc, store := newTestService(t)
	ctx := t.Context()
	scope := Scope{ActorID: "cli:local", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	source, err := svc.PrepareBackground(ctx, scope, BackgroundRequest{Kind: "cron", Name: "job"})
	if err != nil {
		t.Fatal(err)
	}
	pair := sessionTestToolPair(source.ID, "source")
	if err := store.Dialogues().Commit(ctx, storage.DialogueCommit{SessionID: source.ID, ToolPair: pair}); err != nil {
		t.Fatal(err)
	}
	copy, err := svc.CopyBackground(ctx, scope, BackgroundCopyRequest{SourceSessionID: source.ID, Kind: "cron", Name: "copy"})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := store.Messages().ListBySession(ctx, copy.ID)
	if err != nil || len(rows) != 2 {
		t.Fatalf("copy=%+v %v", rows, err)
	}
	resultID, err := storage.ToolResultMessageID(rows[0])
	if err != nil || resultID != rows[1].ID || resultID == pair.Result.ID {
		t.Fatalf("copied pair=%+v %v", rows, err)
	}
	if _, err := svc.Resume(ctx, scope, copy.ID); err != nil {
		t.Fatal(err)
	}
	fork, err := svc.Fork(ctx, scope, rows[0].ID)
	if err != nil || fork.ForkFromMessageID != rows[1].ID {
		t.Fatalf("copy fork=%+v %v", fork, err)
	}
}
