package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"

	"elbot/internal/storage"
)

func materialSeed(id string) *storage.NativeSeed {
	return &storage.NativeSeed{ID: id, APIType: "response", Provider: "p", ItemsJSON: `[{"type":"compaction","encrypted_content":"opaque"}]`, MaterialsJSON: `[]`, ContinuationJSON: `[]`, CallsJSON: `[]`}
}

func TestNativeSessionMaterialCreateRollsBackSessionMessagesAndMedia(t *testing.T) {
	store, _ := nativeStorageFixture(t)
	ctx := t.Context()
	id := "media:" + strings.Repeat("a", 64)
	if err := store.Media().Upsert(ctx, &storage.Media{ID: id, MIMEType: "image/png", Backend: "local"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `CREATE TRIGGER reject_seed BEFORE INSERT ON native_seeds BEGIN SELECT RAISE(ABORT,'seed failed'); END`); err != nil {
		t.Fatal(err)
	}
	seed := materialSeed("root")
	seed.MediaIDs = []string{id}
	req := storage.SessionMaterialCreate{Session: &storage.Session{ID: "child", Metadata: `{"llm_origin":{"protocol":"response","provider":"p"}}`}, SourceSessionID: "s", Seed: seed, Messages: []*storage.Message{{ID: "copy", SessionID: "child", Role: storage.RoleAssistant, Segments: `[{"type":"image","media":"` + id + `"}]`}}}
	if err := store.Sessions().CreateMaterial(ctx, req); err == nil {
		t.Fatal("seed failure committed")
	}
	if _, err := store.Sessions().Get(ctx, "child"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("partial session: %v", err)
	}
	if messages, err := store.Messages().ListBySession(ctx, "child"); err != nil || len(messages) != 0 {
		t.Fatalf("partial messages=%+v %v", messages, err)
	}
	for _, owner := range []struct{ kind, id string }{{"message", "copy"}, {"native_seed", "root"}} {
		refs, err := store.MediaReferences().ListByOwner(ctx, owner.kind, owner.id)
		if err != nil || len(refs) != 0 {
			t.Fatalf("partial refs=%+v %v", refs, err)
		}
	}
}

func TestNativeSeedConsumptionIsAtomicWithFirstCheckpoint(t *testing.T) {
	store, _ := nativeStorageFixture(t)
	ctx := t.Context()
	seed := materialSeed("root")
	row := &storage.Session{ID: "child", Metadata: `{"llm_origin":{"protocol":"response","provider":"p"},"unknown":9007199254740993,"context_compact":{"pending":true,"seed_id":"root","keep":true}}`}
	if err := store.Sessions().CreateMaterial(ctx, storage.SessionMaterialCreate{Session: row, Seed: seed}); err != nil {
		t.Fatal(err)
	}
	exchange := &storage.NativeExchange{ID: "child-exchange", SessionID: row.ID, APIType: "response", Provider: "p", RequestJSON: `{"input":[]}`, InputIDsJSON: `[]`}
	if err := store.Dialogues().CreateExchange(ctx, exchange); err != nil {
		t.Fatal(err)
	}
	if err := store.Dialogues().FinishExchange(ctx, exchange.ID, "completed", `{"id":"child-response","status":"completed","output":[]}`, `[]`, ""); err != nil {
		t.Fatal(err)
	}
	commit := storage.DialogueCommit{SessionID: row.ID, Messages: []*storage.Message{{ID: "reply", SessionID: row.ID, Role: storage.RoleAssistant, Content: "reply"}}, Native: &storage.NativeCommit{Checkpoint: storage.NativeCheckpoint{ID: "child-checkpoint", SessionID: row.ID, ExchangeID: exchange.ID, ResponseID: "child-response", MessageID: "reply", SeedID: seed.ID}, ConsumeSeedID: seed.ID}}
	if _, err := store.db.ExecContext(ctx, `CREATE TRIGGER reject_seed_consumption BEFORE UPDATE OF consumed ON native_seeds BEGIN SELECT RAISE(ABORT,'consume failed'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.Dialogues().Commit(ctx, commit); err == nil {
		t.Fatal("consumption failure committed")
	}
	if checkpoint, err := store.Dialogues().CurrentCheckpoint(ctx, row.ID); err != nil || checkpoint != nil {
		t.Fatalf("partial checkpoint=%+v %v", checkpoint, err)
	}
	if current, err := store.Dialogues().Seed(ctx, row.ID); err != nil || current.Consumed {
		t.Fatalf("consumed failed seed=%+v %v", current, err)
	}
	if messages, err := store.Messages().ListBySession(ctx, row.ID); err != nil || len(messages) != 0 {
		t.Fatalf("partial reply=%+v %v", messages, err)
	}
	if _, err := store.db.ExecContext(ctx, `DROP TRIGGER reject_seed_consumption`); err != nil {
		t.Fatal(err)
	}
	if err := store.Dialogues().Commit(ctx, commit); err != nil {
		t.Fatal(err)
	}
	if current, err := store.Dialogues().Seed(ctx, row.ID); err != nil || !current.Consumed {
		t.Fatalf("unconsumed seed=%+v %v", current, err)
	}
	persisted, err := store.Sessions().Get(ctx, row.ID)
	if err != nil || strings.Contains(persisted.Metadata, `"pending":true`) || !strings.Contains(persisted.Metadata, `"keep":true`) || !strings.Contains(persisted.Metadata, "9007199254740993") {
		t.Fatalf("metadata=%+v %v", persisted, err)
	}
}

func TestNativeHistoricalCallSnapshotDoesNotFollowExecutionUpdates(t *testing.T) {
	store, _ := nativeStorageFixture(t)
	ctx := t.Context()
	commit := nativeStorageCommit("head", "")
	call := storage.NativeCall{ExchangeID: "exchange", CallID: "call", Name: "once", Arguments: `{}`, Status: "pending"}
	commit.Native.Calls = []storage.NativeCall{call}
	if err := store.Dialogues().Commit(ctx, commit); err != nil {
		t.Fatal(err)
	}
	call.Status, call.ResultInputID = "completed", "result"
	if err := store.Dialogues().Commit(ctx, storage.DialogueCommit{SessionID: "s", Native: &storage.NativeCommit{ExpectedCheckpointID: "head", Calls: []storage.NativeCall{call}, Inputs: []storage.NativeInput{{ID: "result", SessionID: "s", ExchangeID: "exchange", CallID: "call", ItemJSON: `{"type":"function_call_output","call_id":"call","output":"later result"}`, MediaJSON: `[]`}}}}); err != nil {
		t.Fatal(err)
	}
	cp, err := store.Dialogues().CheckpointForMessage(ctx, "s", "message-head")
	if err != nil || !strings.Contains(cp.CallsJSON, "pending") || strings.Contains(cp.CallsJSON, "completed") || strings.Contains(cp.CallsJSON, "result") {
		t.Fatalf("historical calls changed=%+v %v", cp, err)
	}
	calls, err := store.Dialogues().Calls(ctx, "exchange")
	if err != nil || calls[0].Status != "completed" {
		t.Fatalf("live call did not advance=%+v %v", calls, err)
	}
}

func TestNativeMaterialCreateRechecksSourceCheckpoint(t *testing.T) {
	store, _ := nativeStorageFixture(t)
	ctx := context.Background()
	if err := store.Dialogues().Commit(ctx, nativeStorageCommit("old", "")); err != nil {
		t.Fatal(err)
	}
	req := storage.SessionMaterialCreate{Session: &storage.Session{ID: "child"}, Seed: materialSeed("root"), SourceSessionID: "s", ExpectedCheckpointID: "old"}
	if err := store.Dialogues().Commit(ctx, nativeStorageCommit("new", "old")); err != nil {
		t.Fatal(err)
	}
	if err := store.Sessions().CreateMaterial(ctx, req); err == nil {
		t.Fatal("late material replaced a newer source snapshot")
	}
	if _, err := store.Sessions().Get(ctx, "child"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("late child=%v", err)
	}
}
