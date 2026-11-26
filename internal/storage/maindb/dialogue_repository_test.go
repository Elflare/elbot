package maindb

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/signal"
)

func TestNativeInputMediaValidation(t *testing.T) {
	for _, raw := range []string{"", "[]", "[", `{}`, `[{"segments":false}]`} {
		t.Run(raw, func(t *testing.T) {
			store, _ := nativeStorageFixture(t)
			commit := nativeStorageCommit("media", "")
			commit.Native.Inputs = []NativeInput{{ID: "input", SessionID: "s", ItemJSON: `{}`, MediaJSON: raw}}
			err := store.CommitDialogue(t.Context(), commit)
			valid := raw == "" || raw == "[]"
			if valid && err != nil || !valid && err == nil {
				t.Fatal("result= err=", err)
			}
			for _, table := range []string{"messages", "native_inputs"} {
				var count int
				if err := store.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if valid && count != 1 || !valid && count != 0 {
					t.Fatalf("%s count=%d", table, count)
				}
			}
		})
	}
}

func TestNativeInputConsumptionRequiresCheckpoint(t *testing.T) {
	store, _ := nativeStorageFixture(t)
	commit := DialogueCommit{SessionID: "s", Native: &NativeCommit{ConsumedInputs: []string{"one", "two"}}}
	err := store.CommitDialogue(t.Context(), commit)
	if err == nil || err.Error() != "input consumption requires a checkpoint" {
		t.Fatal("result= err=", err)
	}
}

func TestSuccessfulIDsBatchBoundaries(t *testing.T) {
	store, _ := nativeStorageFixture(t)
	var ids []string
	now := time.Now()
	for i := range 501 {
		id := fmt.Sprint(i)
		ids = append(ids, id)
		if err := store.CreateToolCallRecord(t.Context(), &ToolCallRecord{SessionID: "s", ToolCallID: id, Success: true, StartedAt: now, FinishedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	for _, size := range []int{0, 500, 501} {
		input := append([]string{}, ids[:size]...)
		if size > 0 {
			input[size-1] = " " + input[size-1] + " "
			input = append(input, ids[0], " ")
		}
		got, err := store.SuccessfulIDs(t.Context(), "s", input)
		if err != nil || len(got) != size {
			t.Fatalf("size=%d results=%d err=%v", size, len(got), err)
		}
		for _, id := range ids[:size] {
			if !got[id] {
				t.Fatalf("missing %s", id)
			}
		}
	}
}

func nativeStorageFixture(t *testing.T) (*Store, *NativeExchange) {
	t.Helper()
	ctx := t.Context()
	store, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	row := &Session{ID: "s", Metadata: `{"unknown":9007199254740993,"workspace":{"keep":true},"llm_origin":{"protocol":"response","provider":"p"}}`}
	if err := store.CreateSession(ctx, row); err != nil {
		t.Fatal(err)
	}
	exchange := &NativeExchange{Status: "pending", ID: "exchange", SessionID: "s", APIType: "response", Provider: "p", Model: "m", RequestJSON: `{"input":[],"unknown":9007199254740993}`}
	if err := store.CreateExchange(ctx, exchange); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishExchange(ctx, exchange.ID, "completed", `{"id":"r","status":"completed","output":[{"type":"reasoning","encrypted_content":"opaque"}]}`, `[{"type":"unknown","keep":true}]`, ""); err != nil {
		t.Fatal(err)
	}
	return store, exchange
}

func nativeStorageCommit(id, parent string) DialogueCommit {
	return DialogueCommit{SessionID: "s", Messages: []*Message{{ID: "message-" + id, SessionID: "s", Role: llm.RoleAssistant, Content: "reply"}}, Native: &NativeCommit{ExpectedCheckpointID: parent, Checkpoint: NativeCheckpoint{ID: id, SessionID: "s", ParentID: parent, ExchangeID: "exchange", ResponseID: "r", MessageID: "message-" + id}}}
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
			if err := store.CommitDialogue(ctx, commit); err == nil {
				t.Fatal("expected rejected transaction")
			}
			row, err := store.GetSession(ctx, "s")
			if err != nil || strings.Contains(row.Metadata, "elyph_rules_shown") {
				t.Fatalf("failed commit marked rules: %+v %v", row, err)
			}
			messages, err := store.ListSessionMessages(ctx, "s")
			if err != nil || len(messages) != 0 {
				t.Fatalf("partial messages: %+v %v", messages, err)
			}
			if _, err := store.db.ExecContext(ctx, `DROP TRIGGER reject_rules`); err != nil {
				t.Fatal(err)
			}
			if err := store.CommitDialogue(ctx, commit); err != nil {
				t.Fatal(err)
			}
			row, err = store.GetSession(ctx, "s")
			if err != nil || !strings.Contains(row.Metadata, `"elyph_rules_shown":true`) || !strings.Contains(row.Metadata, "9007199254740993") {
				t.Fatalf("committed state: %+v %v", row, err)
			}
			messages, err = store.ListSessionMessages(ctx, "s")
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
	CreateMediaForTest(t, store, mediaID, "image/png")
	commit.Messages[0].Segments = `[{"type":"image","media":"` + mediaID + `"}]`
	commit.Native.Inputs = []NativeInput{{ID: "input", SessionID: "s", ItemJSON: `{"type":"message","role":"user","content":[]}`, MediaJSON: `[]`}}
	if err := store.CommitDialogue(t.Context(), commit); err == nil {
		t.Fatal("checkpoint IO failure: result= err=", err)
	}
	if rows, err := store.ListSessionMessages(t.Context(), "s"); err != nil || len(rows) != 0 {
		t.Fatalf("partial messages=%+v %v", rows, err)
	}
	if rows, err := store.PendingInputs(t.Context(), "s"); err != nil || len(rows) != 0 {
		t.Fatalf("partial input=%+v %v", rows, err)
	}
	if refs, err := store.ListByOwner(t.Context(), "message", "message-c"); err != nil || len(refs) != 0 {
		t.Fatalf("partial media references=%+v %v", refs, err)
	}
	if checkpoint, err := store.CurrentCheckpoint(t.Context(), "s"); err != nil || checkpoint != nil {
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
			CreateMediaForTest(t, store, mediaID, "image/png")
			batch := &ToolBatch{Call: &Message{ID: "batch", SessionID: "s", Role: llm.RoleAssistant,
				ToolCalls: []llm.ToolCallRequest{{ID: "A"}, {ID: "B"}, {ID: "C"}}, Metadata: `{"tool_result_message_ids":["result-A","result-B","result-C"]}`}}
			for _, id := range []string{"A", "B", "C"} {
				batch.Results = append(batch.Results, &Message{ID: "result-" + id, SessionID: "s", Role: llm.RoleTool, ToolCallID: id, Content: id,
					Segments: `[{"type":"image","media":"` + mediaID + `"}]`})
			}
			err := store.CommitDialogue(ctx, DialogueCommit{SessionID: "s", ToolBatch: batch})
			if (err != nil) != fail {
				t.Fatalf("commit error=%v", err)
			}
			rows, err := store.ListSessionMessages(ctx, "s")
			want := 4
			if fail {
				want = 0
			}
			if err != nil || len(rows) != want {
				t.Fatalf("partial batch=%+v err=%v", rows, err)
			}
			for _, result := range batch.Results {
				refs, err := store.ListByOwner(ctx, "tool_result", result.ID)
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
	for _, commit := range []DialogueCommit{
		{SessionID: "s", Messages: []*Message{{SessionID: "other"}}},
		{SessionID: "s", Messages: []*Message{{SessionID: "s", Segments: "["}}},
		nativeStorageCommit("changed", "obsolete"),
	} {
		err := store.CommitDialogue(t.Context(), commit)
		if err == nil {
			t.Fatal("validation: result= err=", err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	err := store.CommitDialogue(t.Context(), DialogueCommit{SessionID: "s"})
	if err == nil {
		t.Fatal("IO: result= err=", err)
	}
}

func TestNativeCheckpointCompareAndSwapPreservesMetadataAndConsumesInput(t *testing.T) {
	store, _ := nativeStorageFixture(t)
	ctx := t.Context()
	input := NativeInput{ID: "input", SessionID: "s", ItemJSON: `{"type":"message","role":"user","content":[]}`, MediaJSON: `[]`}
	if err := store.CommitDialogue(ctx, DialogueCommit{SessionID: "s", Native: &NativeCommit{Inputs: []NativeInput{input}}}); err != nil {
		t.Fatal(err)
	}
	commit := nativeStorageCommit("c1", "")
	commit.Native.ConsumedInputs = []string{input.ID}
	if err := store.CommitDialogue(ctx, commit); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitDialogue(ctx, nativeStorageCommit("late", "")); err == nil {
		t.Fatal("late cursor replaced a newer commit")
	}
	if rows, err := store.ListSessionMessages(ctx, "s"); err != nil || len(rows) != 1 || rows[0].ID != "message-c1" {
		t.Fatalf("late message=%+v %v", rows, err)
	}
	if rows, err := store.PendingInputs(ctx, "s"); err != nil || len(rows) != 0 {
		t.Fatalf("input consumption=%+v %v", rows, err)
	}
	row, err := store.GetSession(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{`9007199254740993`, `"workspace":{"keep":true}`, `"llm_origin":{"protocol":"response","provider":"p"}`} {
		if !strings.Contains(row.Metadata, text) {
			t.Fatalf("lost %s: %s", text, row.Metadata)
		}
	}
	checkpoint, err := store.CurrentCheckpoint(ctx, "s")
	if err != nil || checkpoint.ID != "c1" {
		t.Fatalf("cursor=%+v %v", checkpoint, err)
	}
}

func TestNativeFailedExchangeCannotBecomeCheckpointAndTerminalIsImmutable(t *testing.T) {
	store, _ := nativeStorageFixture(t)
	ctx := t.Context()
	if err := store.FinishExchange(ctx, "exchange", "failed", `{}`, `[]`, "late"); err == nil {
		t.Fatal("terminal facts overwritten")
	}
	exchange := &NativeExchange{Status: "pending", ID: "failed", SessionID: "s", APIType: "response", Provider: "p", RequestJSON: `{}`}
	if err := store.CreateExchange(ctx, exchange); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishExchange(ctx, "failed", "incomplete", `{"status":"incomplete"}`, `[]`, "limit"); err != nil {
		t.Fatal(err)
	}
	commit := nativeStorageCommit("c", "")
	commit.Native.Checkpoint.ExchangeID = "failed"
	if err := store.CommitDialogue(ctx, commit); err == nil {
		t.Fatal("incomplete response became checkpoint")
	}
	if rows, err := store.ListSessionMessages(ctx, "s"); err != nil || len(rows) != 0 {
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
			pair := &ToolBatch{
				Call:    &Message{ID: "head", SessionID: "s", Role: llm.RoleAssistant, ToolCalls: []llm.ToolCallRequest{{ID: "one", Name: "tool", Arguments: "{}"}}, Metadata: `{"tool_result_message_ids":["result"]}`},
				Results: []*Message{{ID: "result", SessionID: "s", Role: llm.RoleTool, ToolCallID: "one", Content: "done"}},
			}
			mediaID := "media:" + strings.Repeat("b", 64)
			CreateMediaForTest(t, store, mediaID, "image/png")
			pair.Call.Segments = `[{"type":"image","media":"` + mediaID + `"}]`
			pair.Results[0].Segments = pair.Call.Segments
			err := store.CommitDialogue(ctx, DialogueCommit{SessionID: "s", ToolBatch: pair})
			if (err != nil) != fail {
				t.Fatalf("commit error=%v", err)
			}
			rows, err := store.ListSessionMessages(ctx, "s")
			want := 2
			if fail {
				want = 0
			}
			if err != nil || len(rows) != want {
				t.Fatalf("partial tool pair=%+v error=%v", rows, err)
			}
			for owner, id := range map[string]string{"message": pair.Call.ID, "tool_result": pair.Results[0].ID} {
				refs, err := store.ListByOwner(ctx, owner, id)
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

func TestDialogueStorageFaultLoggingCannotReplaceBusinessError(t *testing.T) {
	store, _ := nativeStorageFixture(t)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	fault := errors.New("log rejected")
	var records []events.LogRecord
	c, err := events.LogSubmitted.Observe(func(_ context.Context, r events.LogRecord) {
		if r.Module == "storage" {
			records = append(records, r)
			return
		}

	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Disconnect()
	err = store.CommitDialogue(t.Context(), nativeStorageCommit("fault", ""))
	if err == nil || errors.Is(err, fault) || len(records) != 0 {
		t.Fatalf("err=%v records=%+v", err, records)
	}
}
