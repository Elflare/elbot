package sqlite

import (
	"context"
	"strings"
	"testing"

	"elbot/internal/storage"
)

func nativeStorageFixture(t *testing.T) (*Store, *storage.NativeExchange) {
	t.Helper()
	ctx := t.Context()
	store, err := New(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	row := &storage.Session{ID: "s", Metadata: `{"unknown":9007199254740993,"workspace":{"keep":true},"llm_origin":{"protocol":"response","provider":"p"}}`}
	if err := store.Sessions().Create(ctx, row); err != nil {
		t.Fatal(err)
	}
	exchange := &storage.NativeExchange{ID: "exchange", SessionID: "s", Protocol: "response", Provider: "p", Model: "m", RequestJSON: `{"input":[],"unknown":9007199254740993}`}
	if err := store.Dialogues().CreateExchange(ctx, exchange); err != nil {
		t.Fatal(err)
	}
	if err := store.Dialogues().FinishExchange(ctx, exchange.ID, "completed", `{"id":"r","status":"completed","output":[{"type":"reasoning","encrypted_content":"opaque"}]}`, `[{"type":"unknown","keep":true}]`, ""); err != nil {
		t.Fatal(err)
	}
	return store, exchange
}

func nativeStorageCommit(id, parent string) storage.DialogueCommit {
	return storage.DialogueCommit{SessionID: "s", Messages: []*storage.Message{{ID: "message-" + id, SessionID: "s", Role: storage.RoleAssistant, Content: "reply"}}, Native: &storage.NativeCommit{ExpectedCheckpointID: parent, Checkpoint: storage.NativeCheckpoint{ID: id, SessionID: "s", ParentID: parent, ExchangeID: "exchange", ResponseID: "r", MessageID: "message-" + id}}}
}

func TestNativeCheckpointCommitRollsBackMessageAndInputOnFailure(t *testing.T) {
	store, _ := nativeStorageFixture(t)
	if _, err := store.db.ExecContext(t.Context(), `CREATE TRIGGER reject_checkpoint BEFORE INSERT ON native_checkpoints BEGIN SELECT RAISE(ABORT,'checkpoint write failed'); END`); err != nil {
		t.Fatal(err)
	}
	commit := nativeStorageCommit("c", "")
	mediaID := "media:" + strings.Repeat("a", 64)
	if err := store.Media().Upsert(t.Context(), &storage.Media{ID: mediaID, MIMEType: "image/png", Backend: "local"}); err != nil {
		t.Fatal(err)
	}
	commit.Messages[0].Segments = `[{"type":"image","media":"` + mediaID + `"}]`
	commit.Native.Inputs = []storage.NativeInput{{ID: "input", SessionID: "s", ItemJSON: `{"type":"message","role":"user","content":[]}`, MediaJSON: `[]`}}
	if err := store.Dialogues().Commit(t.Context(), commit); err == nil {
		t.Fatal("checkpoint failure committed")
	}
	if rows, err := store.Messages().ListBySession(t.Context(), "s"); err != nil || len(rows) != 0 {
		t.Fatalf("partial messages=%+v %v", rows, err)
	}
	if rows, err := store.Dialogues().PendingInputs(t.Context(), "s"); err != nil || len(rows) != 0 {
		t.Fatalf("partial input=%+v %v", rows, err)
	}
	if refs, err := store.MediaReferences().ListByOwner(t.Context(), "message", "message-c"); err != nil || len(refs) != 0 {
		t.Fatalf("partial media references=%+v %v", refs, err)
	}
	if checkpoint, err := store.Dialogues().CurrentCheckpoint(t.Context(), "s"); err != nil || checkpoint != nil {
		t.Fatalf("partial cursor=%+v %v", checkpoint, err)
	}
	if exchange, err := store.Dialogues().GetExchange(t.Context(), "exchange"); err != nil || exchange.Status != "completed" || !strings.Contains(exchange.ResponseJSON, "opaque") {
		t.Fatalf("lost API facts=%+v %v", exchange, err)
	}
}

func TestNativeCheckpointCompareAndSwapPreservesMetadataAndConsumesInput(t *testing.T) {
	store, _ := nativeStorageFixture(t)
	ctx := t.Context()
	input := storage.NativeInput{ID: "input", SessionID: "s", ItemJSON: `{"type":"message","role":"user","content":[]}`, MediaJSON: `[]`}
	if err := store.Dialogues().Commit(ctx, storage.DialogueCommit{SessionID: "s", Native: &storage.NativeCommit{Inputs: []storage.NativeInput{input}}}); err != nil {
		t.Fatal(err)
	}
	commit := nativeStorageCommit("c1", "")
	commit.Native.ConsumedInputs = []string{input.ID}
	if err := store.Dialogues().Commit(ctx, commit); err != nil {
		t.Fatal(err)
	}
	if err := store.Dialogues().Commit(ctx, nativeStorageCommit("late", "")); err == nil {
		t.Fatal("late cursor replaced a newer commit")
	}
	if rows, err := store.Messages().ListBySession(ctx, "s"); err != nil || len(rows) != 1 || rows[0].ID != "message-c1" {
		t.Fatalf("late message=%+v %v", rows, err)
	}
	if rows, err := store.Dialogues().PendingInputs(ctx, "s"); err != nil || len(rows) != 0 {
		t.Fatalf("input consumption=%+v %v", rows, err)
	}
	row, err := store.Sessions().Get(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{`9007199254740993`, `"workspace":{"keep":true}`, `"llm_origin":{"protocol":"response","provider":"p"}`} {
		if !strings.Contains(row.Metadata, text) {
			t.Fatalf("lost %s: %s", text, row.Metadata)
		}
	}
	checkpoint, err := store.Dialogues().CurrentCheckpoint(ctx, "s")
	if err != nil || checkpoint.ID != "c1" {
		t.Fatalf("cursor=%+v %v", checkpoint, err)
	}
}

func TestNativeFailedExchangeCannotBecomeCheckpointAndTerminalIsImmutable(t *testing.T) {
	store, _ := nativeStorageFixture(t)
	ctx := t.Context()
	if err := store.Dialogues().FinishExchange(ctx, "exchange", "failed", `{}`, `[]`, "late"); err == nil {
		t.Fatal("terminal facts overwritten")
	}
	exchange := &storage.NativeExchange{ID: "failed", SessionID: "s", Protocol: "response", Provider: "p", RequestJSON: `{}`}
	if err := store.Dialogues().CreateExchange(ctx, exchange); err != nil {
		t.Fatal(err)
	}
	if err := store.Dialogues().FinishExchange(ctx, "failed", "incomplete", `{"status":"incomplete"}`, `[]`, "limit"); err != nil {
		t.Fatal(err)
	}
	commit := nativeStorageCommit("c", "")
	commit.Native.Checkpoint.ExchangeID = "failed"
	if err := store.Dialogues().Commit(ctx, commit); err == nil {
		t.Fatal("incomplete response became checkpoint")
	}
	if rows, err := store.Messages().ListBySession(ctx, "s"); err != nil || len(rows) != 0 {
		t.Fatalf("partial incomplete display=%+v %v", rows, err)
	}
}

func TestDialogueToolPreparationUpdatesOnlyItsCall(t *testing.T) {
	store, _ := nativeStorageFixture(t)
	ctx := context.Background()
	head := &storage.Message{ID: "head", SessionID: "s", Role: storage.RoleAssistant, Metadata: `{"tool_calls":[{"ID":"one","Name":"tool","Arguments":"{}"},{"ID":"two","Name":"tool","Arguments":"{}"}],"unknown":9007199254740993}`}
	if err := store.Dialogues().Commit(ctx, storage.DialogueCommit{SessionID: "s", Messages: []*storage.Message{head}}); err != nil {
		t.Fatal(err)
	}
	patch := &storage.ToolCallUpdate{MessageID: "head", Index: 1, Call: []byte(`{"ID":"two","Name":"tool","Arguments":"{\"actual\":true}"}`)}
	if err := store.Dialogues().Commit(ctx, storage.DialogueCommit{SessionID: "s", ToolCall: patch}); err != nil {
		t.Fatal(err)
	}
	row, err := store.Messages().Get(ctx, "head")
	if err != nil || !strings.Contains(row.Metadata, "9007199254740993") || !strings.Contains(row.Metadata, "actual") || !strings.Contains(row.Metadata, `"ID":"one"`) {
		t.Fatalf("tool update=%+v %v", row, err)
	}
}
