package session

import (
	"fmt"
	"testing"

	"elbot/internal/storage"
)

func sessionTestToolPair(sessionID, suffix string) *storage.ToolBatch {
	headID, resultID := "head-"+suffix, "result-"+suffix
	return &storage.ToolBatch{
		Call: &storage.Message{ID: headID, SessionID: sessionID, Role: storage.RoleAssistant,
			Metadata: fmt.Sprintf(`{"tool_calls":[{"ID":%q,"Name":"tool","Arguments":"{}"}],"tool_result_message_ids":[%q]}`, suffix, resultID)},
		Results: []*storage.Message{{ID: resultID, SessionID: sessionID, Role: storage.RoleTool, ToolCallID: suffix, Content: suffix}},
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
	for _, pair := range []*storage.ToolBatch{first, sessionTestToolPair(source.ID, "later")} {
		if _, err := store.Dialogues().Commit(ctx, storage.DialogueCommit{SessionID: source.ID, ToolBatch: pair}); err != nil {
			t.Fatal(err)
		}
	}
	fork, err := svc.Fork(ctx, scope, first.Call.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fork.ForkFromMessageID != first.Results[0].ID {
		t.Fatalf("fork=%+v", fork)
	}
	rows, err := store.Messages().ListBySessionUpTo(ctx, fork.ParentSessionID, fork.ForkFromMessageID)
	if err != nil || len(rows) != 2 || rows[1].ID != first.Results[0].ID {
		t.Fatalf("boundary=%+v %v", rows, err)
	}
}

func TestBackgroundCopyRemapsAllToolBatchLinks(t *testing.T) {
	svc, store := newTestService(t)
	ctx := t.Context()
	scope := Scope{ActorID: "cli:local", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	source, err := svc.PrepareBackground(ctx, scope, BackgroundRequest{Kind: "cron", Name: "job"})
	if err != nil {
		t.Fatal(err)
	}
	pair := sessionTestToolPair(source.ID, "source")
	pair.Call.Metadata = `{"tool_calls":[{"ID":"source"},{"ID":"second"},{"ID":"third"}],"tool_result_message_ids":["result-source","result-second","result-third"]}`
	for _, id := range []string{"second", "third"} {
		pair.Results = append(pair.Results, &storage.Message{ID: "result-" + id, SessionID: source.ID, Role: storage.RoleTool, ToolCallID: id, Content: id})
	}
	if _, err := store.Dialogues().Commit(ctx, storage.DialogueCommit{SessionID: source.ID, ToolBatch: pair}); err != nil {
		t.Fatal(err)
	}
	copy, err := svc.CopyBackground(ctx, scope, BackgroundCopyRequest{SourceSessionID: source.ID, Kind: "cron", Name: "copy"})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := store.Messages().ListBySession(ctx, copy.ID)
	if err != nil || len(rows) != 4 {
		t.Fatalf("copy=%+v %v", rows, err)
	}
	resultID, err := storage.ToolResultMessageIDs(rows[0])
	if err != nil || len(resultID) != 3 {
		t.Fatalf("copied pair=%+v %v", rows, err)
	}
	for i, id := range resultID {
		if id != rows[i+1].ID || id == pair.Results[i].ID || rows[i+1].ToolCallID != pair.Results[i].ToolCallID {
			t.Fatalf("unmapped result %d: %+v", i, rows)
		}
	}
	if _, err := svc.Resume(ctx, scope, copy.ID); err != nil {
		t.Fatal(err)
	}
	fork, err := svc.Fork(ctx, scope, rows[0].ID)
	if err != nil || fork.ForkFromMessageID != rows[3].ID {
		t.Fatalf("copy fork=%+v %v", fork, err)
	}
}
