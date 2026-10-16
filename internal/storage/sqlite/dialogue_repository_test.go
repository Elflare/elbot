package sqlite

import (
	"fmt"
	"strings"
	"testing"

	"elbot/internal/events"
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
	exchange := &storage.NativeExchange{ID: "exchange", SessionID: "s", APIType: "response", Provider: "p", Model: "m", RequestJSON: `{"input":[],"unknown":9007199254740993}`}
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

func TestElyphRulesCommitWithMessageAndNativeState(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(fmt.Sprint(native), func(t *testing.T) {
			store, _ := nativeStorageFixture(t)
			ctx := t.Context()
			commit := nativeStorageCommit("rules", "")
			if !native {
				commit.Native = nil
			}
			commit.ElyphRulesShown = true
			commit.Messages[0].Content = "ELyph rules and business content"
			if _, err := store.db.ExecContext(ctx, `CREATE TRIGGER reject_rules BEFORE UPDATE ON sessions BEGIN SELECT RAISE(ABORT,'reject rules state'); END`); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Dialogues().Commit(ctx, commit); err == nil {
				t.Fatal("expected rejected transaction")
			}
			row, err := store.Sessions().Get(ctx, "s")
			if err != nil || strings.Contains(row.Metadata, "elyph_rules_shown") {
				t.Fatalf("failed commit marked rules: %+v %v", row, err)
			}
			messages, err := store.Messages().ListBySession(ctx, "s")
			if err != nil || len(messages) != 0 {
				t.Fatalf("partial messages: %+v %v", messages, err)
			}
			if _, err := store.db.ExecContext(ctx, `DROP TRIGGER reject_rules`); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Dialogues().Commit(ctx, commit); err != nil {
				t.Fatal(err)
			}
			row, err = store.Sessions().Get(ctx, "s")
			if err != nil || !strings.Contains(row.Metadata, `"elyph_rules_shown":true`) || !strings.Contains(row.Metadata, "9007199254740993") {
				t.Fatalf("committed state: %+v %v", row, err)
			}
			messages, err = store.Messages().ListBySession(ctx, "s")
			if err != nil || len(messages) != 1 || messages[0].Content != commit.Messages[0].Content {
				t.Fatalf("committed messages: %+v %v", messages, err)
			}
			if native && !strings.Contains(row.Metadata, `"llm_checkpoint":"rules"`) {
				t.Fatalf("checkpoint lost: %s", row.Metadata)
			}
		})
	}
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
	if result, err := store.Dialogues().Commit(t.Context(), commit); err == nil || result.ResultStatus != events.ResultFailed {
		t.Fatalf("checkpoint IO failure: result=%+v err=%v", result, err)
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
	var status, response string
	if err := store.db.QueryRowContext(t.Context(), `SELECT status,response_json FROM native_exchanges WHERE id='exchange'`).Scan(&status, &response); err != nil || status != "completed" || !strings.Contains(response, "opaque") {
		t.Fatalf("lost API facts: status=%s response=%s err=%v", status, response, err)
	}
}

func TestToolBatchLastResultFailureRollsBackEveryMessageAndMediaReference(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			store, _ := nativeStorageFixture(t)
			ctx := t.Context()
			if fail {
				if _, err := store.db.ExecContext(ctx, `CREATE TRIGGER reject_last_tool BEFORE INSERT ON messages WHEN NEW.tool_call_id='C' BEGIN SELECT RAISE(ABORT,'last result failed'); END`); err != nil {
					t.Fatal(err)
				}
			}
			mediaID := "media:" + strings.Repeat("c", 64)
			if err := store.Media().Upsert(ctx, &storage.Media{ID: mediaID, MIMEType: "image/png", Backend: "local"}); err != nil {
				t.Fatal(err)
			}
			batch := &storage.ToolBatch{Call: &storage.Message{ID: "batch", SessionID: "s", Role: storage.RoleAssistant,
				Metadata: `{"tool_calls":[{"ID":"A"},{"ID":"B"},{"ID":"C"}],"tool_result_message_ids":["result-A","result-B","result-C"]}`}}
			for _, id := range []string{"A", "B", "C"} {
				batch.Results = append(batch.Results, &storage.Message{ID: "result-" + id, SessionID: "s", Role: storage.RoleTool, ToolCallID: id, Content: id,
					Segments: `[{"type":"image","media":"` + mediaID + `"}]`})
			}
			_, err := store.Dialogues().Commit(ctx, storage.DialogueCommit{SessionID: "s", ToolBatch: batch})
			if (err != nil) != fail {
				t.Fatalf("commit error=%v", err)
			}
			rows, err := store.Messages().ListBySession(ctx, "s")
			want := 4
			if fail {
				want = 0
			}
			if err != nil || len(rows) != want {
				t.Fatalf("partial batch=%+v err=%v", rows, err)
			}
			for _, result := range batch.Results {
				refs, err := store.MediaReferences().ListByOwner(ctx, "tool_result", result.ID)
				want := 1
				if fail {
					want = 0
				}
				if err != nil || len(refs) != want {
					t.Fatalf("partial media refs=%+v err=%v", refs, err)
				}
			}
		})
	}
}

func TestCommitSeparatesInternalValidationFromIOFailure(t *testing.T) {
	store, _ := nativeStorageFixture(t)
	for _, commit := range []storage.DialogueCommit{
		{},
		{SessionID: "s", Messages: []*storage.Message{{SessionID: "other"}}},
		{SessionID: "s", Messages: []*storage.Message{{SessionID: "s", Segments: "["}}},
		nativeStorageCommit("changed", "obsolete"),
	} {
		result, err := store.Dialogues().Commit(t.Context(), commit)
		if err == nil || result.ResultStatus != events.ResultSkipped {
			t.Fatalf("validation: result=%+v err=%v", result, err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := store.Dialogues().Commit(t.Context(), storage.DialogueCommit{SessionID: "s"})
	if err == nil || result.ResultStatus != events.ResultFailed {
		t.Fatalf("IO: result=%+v err=%v", result, err)
	}
}

func TestNativeCheckpointCompareAndSwapPreservesMetadataAndConsumesInput(t *testing.T) {
	store, _ := nativeStorageFixture(t)
	ctx := t.Context()
	input := storage.NativeInput{ID: "input", SessionID: "s", ItemJSON: `{"type":"message","role":"user","content":[]}`, MediaJSON: `[]`}
	if _, err := store.Dialogues().Commit(ctx, storage.DialogueCommit{SessionID: "s", Native: &storage.NativeCommit{Inputs: []storage.NativeInput{input}}}); err != nil {
		t.Fatal(err)
	}
	commit := nativeStorageCommit("c1", "")
	commit.Native.ConsumedInputs = []string{input.ID}
	if _, err := store.Dialogues().Commit(ctx, commit); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Dialogues().Commit(ctx, nativeStorageCommit("late", "")); err == nil {
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
	exchange := &storage.NativeExchange{ID: "failed", SessionID: "s", APIType: "response", Provider: "p", RequestJSON: `{}`}
	if err := store.Dialogues().CreateExchange(ctx, exchange); err != nil {
		t.Fatal(err)
	}
	if err := store.Dialogues().FinishExchange(ctx, "failed", "incomplete", `{"status":"incomplete"}`, `[]`, "limit"); err != nil {
		t.Fatal(err)
	}
	commit := nativeStorageCommit("c", "")
	commit.Native.Checkpoint.ExchangeID = "failed"
	if _, err := store.Dialogues().Commit(ctx, commit); err == nil {
		t.Fatal("incomplete response became checkpoint")
	}
	if rows, err := store.Messages().ListBySession(ctx, "s"); err != nil || len(rows) != 0 {
		t.Fatalf("partial incomplete display=%+v %v", rows, err)
	}
}
func TestDialogueToolPairIsAtomic(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "result_write_failure"}[fail], func(t *testing.T) {
			store, _ := nativeStorageFixture(t)
			ctx := t.Context()
			if fail {
				if _, err := store.db.ExecContext(ctx, `CREATE TRIGGER reject_tool BEFORE INSERT ON messages WHEN NEW.role='tool' BEGIN SELECT RAISE(ABORT,'tool result failed'); END`); err != nil {
					t.Fatal(err)
				}
			}
			pair := &storage.ToolBatch{
				Call:    &storage.Message{ID: "head", SessionID: "s", Role: storage.RoleAssistant, Metadata: `{"tool_calls":[{"ID":"one","Name":"tool","Arguments":"{}"}],"tool_result_message_ids":["result"]}`},
				Results: []*storage.Message{{ID: "result", SessionID: "s", Role: storage.RoleTool, ToolCallID: "one", Content: "done"}},
			}
			mediaID := "media:" + strings.Repeat("b", 64)
			if err := store.Media().Upsert(ctx, &storage.Media{ID: mediaID, MIMEType: "image/png", Backend: "local"}); err != nil {
				t.Fatal(err)
			}
			pair.Call.Segments = `[{"type":"image","media":"` + mediaID + `"}]`
			pair.Results[0].Segments = pair.Call.Segments
			_, err := store.Dialogues().Commit(ctx, storage.DialogueCommit{SessionID: "s", ToolBatch: pair})
			if (err != nil) != fail {
				t.Fatalf("commit error=%v", err)
			}
			rows, err := store.Messages().ListBySession(ctx, "s")
			want := 2
			if fail {
				want = 0
			}
			if err != nil || len(rows) != want {
				t.Fatalf("partial tool pair=%+v error=%v", rows, err)
			}
			for owner, id := range map[string]string{"message": pair.Call.ID, "tool_result": pair.Results[0].ID} {
				refs, err := store.MediaReferences().ListByOwner(ctx, owner, id)
				wantRefs := 1
				if fail {
					wantRefs = 0
				}
				if err != nil || len(refs) != wantRefs {
					t.Fatalf("partial media refs=%+v %v", refs, err)
				}
			}
		})
	}
}
