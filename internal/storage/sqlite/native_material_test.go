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

func TestNativeSeedEmptyMediaIsAnArrayAndPassesAudit(t *testing.T) {
	for _, ids := range [][]string{nil, {}} {
		store, _ := nativeStorageFixture(t)
		seed := materialSeed("empty-root")
		seed.MediaIDs = ids
		row := &storage.Session{ID: "empty-child", Metadata: `{"llm_origin":{"protocol":"response","provider":"p"}}`}
		if _, err := store.Sessions().CreateMaterial(t.Context(), storage.SessionMaterialCreate{Session: row, Seed: seed}); err != nil {
			t.Fatal(err)
		}
		var raw string
		if err := store.db.QueryRow(`SELECT media_ids_json FROM native_seeds WHERE id=?`, seed.ID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if raw != "[]" {
			t.Fatalf("empty media list = %s, want []", raw)
		}
		if issues, err := store.Media().CheckReferences(t.Context()); err != nil || len(issues) != 0 {
			t.Fatalf("empty seed audit = %v, %v", issues, err)
		}
	}
}

func TestNativeSeedMediaMigrationRepairsNullAndPreservesReferences(t *testing.T) {
	store, _ := nativeStorageFixture(t)
	ctx := t.Context()
	id := "media:" + strings.Repeat("b", 64)
	if err := store.Media().Upsert(ctx, &storage.Media{ID: id, Backend: "local", MIMEType: "image/png"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"empty", "with-media"} {
		seed := materialSeed(name)
		if name == "with-media" {
			seed.MediaIDs = []string{id}
		}
		row := &storage.Session{ID: name, Metadata: `{"llm_origin":{"protocol":"response","provider":"p"}}`}
		if _, err := store.Sessions().CreateMaterial(ctx, storage.SessionMaterialCreate{Session: row, Seed: seed}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.Exec(`UPDATE native_seeds SET media_ids_json='null',consumed=1 WHERE id='empty'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DELETE FROM schema_migrations WHERE version=23`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := runMigrations(ctx, store.db); err != nil {
			t.Fatal(err)
		}
	}
	var raw string
	var consumed bool
	if err := store.db.QueryRow(`SELECT media_ids_json,consumed FROM native_seeds WHERE id='empty'`).Scan(&raw, &consumed); err != nil || raw != "[]" || !consumed {
		t.Fatalf("migrated seed media=%s consumed=%v error=%v", raw, consumed, err)
	}
	if issues, err := store.Media().CheckReferences(ctx); err != nil || len(issues) != 0 {
		t.Fatalf("migrated audit = %v, %v", issues, err)
	}
	if _, err := store.db.Exec(`DELETE FROM media_references WHERE owner_type='native_seed' AND owner_id='with-media'`); err != nil {
		t.Fatal(err)
	}
	issues, err := store.Media().CheckReferences(ctx)
	if err != nil || len(issues) != 1 || !strings.Contains(issues[0], "with-media") {
		t.Fatalf("missing real reference audit = %v, %v", issues, err)
	}
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
	if _, err := store.Sessions().CreateMaterial(ctx, req); err == nil {
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
	if _, err := store.Sessions().CreateMaterial(ctx, storage.SessionMaterialCreate{Session: row, Seed: seed}); err != nil {
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
	if _, err := store.Dialogues().Commit(ctx, commit); err == nil {
		t.Fatal("consumption failure committed")
	}
	if checkpoint, err := store.Dialogues().CurrentCheckpoint(ctx, row.ID); err != nil || checkpoint != nil {
		t.Fatalf("partial checkpoint=%+v %v", checkpoint, err)
	}
	var consumed bool
	if err := store.db.QueryRowContext(ctx, `SELECT consumed FROM native_seeds WHERE id=? AND session_id=?`, seed.ID, row.ID).Scan(&consumed); err != nil || consumed {
		t.Fatalf("consumed failed seed=%t %v", consumed, err)
	}
	if messages, err := store.Messages().ListBySession(ctx, row.ID); err != nil || len(messages) != 0 {
		t.Fatalf("partial reply=%+v %v", messages, err)
	}
	if _, err := store.db.ExecContext(ctx, `DROP TRIGGER reject_seed_consumption`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Dialogues().Commit(ctx, commit); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT consumed FROM native_seeds WHERE id=? AND session_id=?`, seed.ID, row.ID).Scan(&consumed); err != nil || !consumed {
		t.Fatalf("unconsumed seed=%t %v", consumed, err)
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
	if _, err := store.Dialogues().Commit(ctx, commit); err != nil {
		t.Fatal(err)
	}
	call.Status, call.ResultInputID = "completed", "result"
	if _, err := store.Dialogues().Commit(ctx, storage.DialogueCommit{SessionID: "s", Native: &storage.NativeCommit{ExpectedCheckpointID: "head", Calls: []storage.NativeCall{call}, Inputs: []storage.NativeInput{{ID: "result", SessionID: "s", ExchangeID: "exchange", CallID: "call", ItemJSON: `{"type":"function_call_output","call_id":"call","output":"later result"}`, MediaJSON: `[]`}}}}); err != nil {
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
	if _, err := store.Dialogues().Commit(ctx, nativeStorageCommit("old", "")); err != nil {
		t.Fatal(err)
	}
	req := storage.SessionMaterialCreate{Session: &storage.Session{ID: "child"}, Seed: materialSeed("root"), SourceSessionID: "s", ExpectedCheckpointID: "old"}
	if _, err := store.Dialogues().Commit(ctx, nativeStorageCommit("new", "old")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Sessions().CreateMaterial(ctx, req); err == nil {
		t.Fatal("late material replaced a newer source snapshot")
	}
	if _, err := store.Sessions().Get(ctx, "child"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("late child=%v", err)
	}
}

func TestLoadNativeWindowKeepsTargetAncestryAndSnapshotResults(t *testing.T) {
	store, _ := nativeStorageFixture(t)
	ctx := t.Context()
	root := nativeStorageCommit("root", "")
	call := storage.NativeCall{ExchangeID: "exchange", CallID: "call", Name: "once", Arguments: `{}`, Status: "completed", ResultInputID: "original-result"}
	root.Native.Calls = []storage.NativeCall{call}
	root.Native.Inputs = []storage.NativeInput{
		{ID: "consumed", SessionID: "s", ItemJSON: `{"type":"message","role":"user","content":[]}`, MediaJSON: `[]`, ConsumedBy: "exchange"},
		{ID: call.ResultInputID, SessionID: "s", ExchangeID: "exchange", CallID: call.CallID, ItemJSON: `{"type":"function_call_output","call_id":"call","output":"original"}`, MediaJSON: `[]`},
	}
	if _, err := store.Dialogues().Commit(ctx, root); err != nil {
		t.Fatal(err)
	}
	call.ResultInputID = "later-result"
	if _, err := store.Dialogues().Commit(ctx, storage.DialogueCommit{SessionID: "s", Native: &storage.NativeCommit{ExpectedCheckpointID: "root", Calls: []storage.NativeCall{call}, Inputs: []storage.NativeInput{{ID: call.ResultInputID, SessionID: "s", ExchangeID: "exchange", CallID: call.CallID, ItemJSON: `{"type":"function_call_output","call_id":"call","output":"later"}`, MediaJSON: `[]`}}}}); err != nil {
		t.Fatal(err)
	}
	nextExchange := &storage.NativeExchange{ID: "next-exchange", SessionID: "s", APIType: "response", Provider: "p", RequestJSON: `{}`, InputIDsJSON: `[]`, PreviousCheckpointID: "root"}
	if err := store.Dialogues().CreateExchange(ctx, nextExchange); err != nil {
		t.Fatal(err)
	}
	if err := store.Dialogues().FinishExchange(ctx, nextExchange.ID, "completed", `{"id":"next-response","status":"completed"}`, `[]`, ""); err != nil {
		t.Fatal(err)
	}
	next := nativeStorageCommit("next", "root")
	next.Native.Checkpoint.ExchangeID, next.Native.Checkpoint.ResponseID = nextExchange.ID, "next-response"
	if _, err := store.Dialogues().Commit(ctx, next); err != nil {
		t.Fatal(err)
	}
	row, err := store.Sessions().Get(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"root", "next"} {
		checkpoint, err := store.Dialogues().GetCheckpoint(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		records, err := store.Dialogues().LoadNativeWindow(ctx, row, checkpoint)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := records.Checkpoints["root"]; !ok || records.Inputs["consumed"].ConsumedBy != "exchange" {
			t.Fatalf("lost ancestry/input: %+v", records)
		}
		if id == "root" {
			if _, ok := records.Checkpoints["next"]; ok {
				t.Fatal("historical target included its descendant")
			}
			if _, ok := records.Exchanges[nextExchange.ID]; ok {
				t.Fatal("historical target included a later exchange")
			}
			if records.Inputs["original-result"].ItemJSON == "" {
				t.Fatal("historical target lost its original result")
			}
			if _, ok := records.Inputs["later-result"]; ok {
				t.Fatal("historical target followed mutable live call state")
			}
		} else if records.Exchanges[nextExchange.ID].ResponseJSON == "" {
			t.Fatal("latest target lost its own exchange")
		}
	}
}
