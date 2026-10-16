package sqlite

import (
	"strings"
	"testing"

	"elbot/internal/storage"
)

func TestToolResultStatisticsFailureRollsBackSlots(t *testing.T) {
	store, _ := nativeStorageFixture(t)
	ctx := t.Context()
	head := &storage.Message{ID: "head", SessionID: "s", Role: storage.RoleAssistant, Metadata: `{"tool_calls":[{"ID":"one"}],"tool_result_message_ids":["result"],"tool_batch_status":"pending"}`}
	result := &storage.Message{ID: "result", SessionID: "s", Role: storage.RoleTool, ToolCallID: "one", Content: "unknown", Metadata: `{"tool_result_status":"pending"}`}
	if _, err := store.Dialogues().Commit(ctx, storage.DialogueCommit{SessionID: "s", ToolBatch: &storage.ToolBatch{Call: head, Results: []*storage.Message{result}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `CREATE TRIGGER reject_tool_statistics BEFORE INSERT ON tool_call_records BEGIN SELECT RAISE(ABORT,'statistics failed'); END`); err != nil {
		t.Fatal(err)
	}
	head.Metadata = strings.ReplaceAll(head.Metadata, "pending", "completed")
	result.Content, result.Metadata = "done", `{"tool_result_status":"completed"}`
	commit := storage.DialogueCommit{SessionID: "s", ToolResult: &storage.ToolResultCommit{Call: head, Result: result, Record: &storage.ToolCallRecord{ToolCallID: "one", ToolName: "tool", Success: true, StartedAt: storage.Now(), FinishedAt: storage.Now()}}}
	if _, err := store.Dialogues().Commit(ctx, commit); err == nil {
		t.Fatal("expected statistics failure")
	}
	rows, err := store.Messages().ListBySession(ctx, "s")
	if err != nil || len(rows) != 2 || rows[1].Content != "unknown" || !strings.Contains(rows[0].Metadata, `"tool_batch_status":"pending"`) {
		t.Fatalf("partially committed result: %+v %v", rows, err)
	}
	if _, err := store.db.ExecContext(ctx, `DROP TRIGGER reject_tool_statistics`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Dialogues().Commit(ctx, commit); err != nil {
		t.Fatal(err)
	}
	usage, err := store.ToolCalls().UsageBySession(ctx, "s")
	if err != nil || len(usage) != 1 {
		t.Fatalf("statistics=%+v %v", usage, err)
	}
}

func TestNativeMessageSnapshotCommitsWithPairWithoutAdvancingCursor(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "rollback"}[fail], func(t *testing.T) {
			store, _ := nativeStorageFixture(t)
			ctx := t.Context()
			initial := nativeStorageCommit("live", "")
			initial.Messages = nil
			initial.Native.Checkpoint.MessageID = ""
			initial.Native.Calls = []storage.NativeCall{{ExchangeID: "exchange", CallID: "one", Name: "tool", Arguments: "{}", Status: "started"}, {ExchangeID: "exchange", CallID: "two", Name: "tool", Arguments: "{}", Status: "pending", Ordinal: 1}}
			if _, err := store.Dialogues().Commit(ctx, initial); err != nil {
				t.Fatal(err)
			}
			if fail {
				if _, err := store.db.ExecContext(ctx, `CREATE TRIGGER reject_snapshot BEFORE INSERT ON native_checkpoints BEGIN SELECT RAISE(ABORT,'snapshot failed'); END`); err != nil {
					t.Fatal(err)
				}
			}
			pair := &storage.ToolBatch{
				Call:    &storage.Message{ID: "head", SessionID: "s", Role: storage.RoleAssistant, Metadata: `{"tool_calls":[{"ID":"one","Name":"tool","Arguments":"{}"}],"tool_result_message_ids":["result"]}`},
				Results: []*storage.Message{{ID: "result", SessionID: "s", Role: storage.RoleTool, ToolCallID: "one", Content: "done"}},
			}
			call := initial.Native.Calls[0]
			call.Status, call.ResultInputID = "completed", "output"
			input := storage.NativeInput{ID: "output", SessionID: "s", MessageID: "result", ExchangeID: "exchange", CallID: "one", ItemJSON: `{"type":"function_call_output","call_id":"one","output":"done"}`, MediaJSON: `[]`}
			commit := storage.DialogueCommit{SessionID: "s", ToolBatch: pair, Native: &storage.NativeCommit{ExpectedCheckpointID: "live", Calls: []storage.NativeCall{call}, Inputs: []storage.NativeInput{input}, Snapshot: &storage.NativeCheckpoint{ID: "snapshot", SessionID: "s", ExchangeID: "exchange", ResponseID: "r", MessageID: "head"}}}
			_, err := store.Dialogues().Commit(ctx, commit)
			if (err != nil) != fail {
				t.Fatalf("snapshot error=%v", err)
			}
			current, err := store.Dialogues().CurrentCheckpoint(ctx, "s")
			if err != nil || current.ID != "live" || strings.Contains(current.CallsJSON, "completed") {
				t.Fatalf("live cursor changed=%+v %v", current, err)
			}
			rows, err := store.Messages().ListBySession(ctx, "s")
			if err != nil {
				t.Fatal(err)
			}
			inputs, err := store.Dialogues().PendingInputs(ctx, "s")
			if err != nil {
				t.Fatal(err)
			}
			calls, err := store.Dialogues().Calls(ctx, "exchange")
			if err != nil {
				t.Fatal(err)
			}
			if fail {
				if len(rows) != 0 || len(inputs) != 0 || calls[0].Status != "started" {
					t.Fatalf("partial snapshot: messages=%+v inputs=%+v calls=%+v", rows, inputs, calls)
				}
				return
			}
			if len(rows) != 2 || len(inputs) != 1 || calls[0].Status != "completed" {
				t.Fatalf("missing snapshot transaction: %+v %+v %+v", rows, inputs, calls)
			}
			cp, err := store.Dialogues().CheckpointForMessage(ctx, "s", "head")
			if err != nil || cp.ID != "snapshot" || !strings.Contains(cp.CallsJSON, `"ResultInputID":"output"`) || !strings.Contains(cp.CallsJSON, `"Status":"pending"`) {
				t.Fatalf("snapshot=%+v %v", cp, err)
			}
			calls[1].Status = "started"
			if _, err := store.Dialogues().Commit(ctx, storage.DialogueCommit{SessionID: "s", Native: &storage.NativeCommit{ExpectedCheckpointID: "live", Calls: []storage.NativeCall{calls[1]}}}); err != nil {
				t.Fatal(err)
			}
			frozen, err := store.Dialogues().GetCheckpoint(ctx, cp.ID)
			if err != nil || frozen.CallsJSON != cp.CallsJSON {
				t.Fatalf("historical snapshot changed=%+v %v", frozen, err)
			}
		})
	}
}
