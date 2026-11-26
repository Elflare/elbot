package maindb

import (
	"strings"
	"testing"
	"time"
)

func TestToolResultStatisticsFailureRollsBackSlots(t *testing.T) {
	store, _ := nativeStorageFixture(t)
	ctx := t.Context()
	head := &Message{ID: "head", SessionID: "s", Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "one"}}, ToolBatchStatus: "pending", Metadata: `{"tool_result_message_ids":["result"]}`}
	result := &Message{ID: "result", SessionID: "s", Role: RoleTool, ToolCallID: "one", Content: "unknown", ToolResultStatus: "pending"}
	if err := store.CommitDialogue(ctx, DialogueCommit{SessionID: "s", ToolBatch: &ToolBatch{Call: head, Results: []*Message{result}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `CREATE TRIGGER reject_tool_statistics BEFORE INSERT ON tool_call_records BEGIN SELECT RAISE(ABORT,'statistics failed'); END`); err != nil {
		t.Fatal(err)
	}
	head.Metadata = strings.ReplaceAll(head.Metadata, "pending", "completed")
	result.Content, result.ToolResultStatus = "done", "completed"
	commit := DialogueCommit{SessionID: "s", ToolResult: &ToolResultCommit{Call: head, Result: result, Record: &ToolCallRecord{ToolCallID: "one", ToolName: "tool", Success: true, StartedAt: time.Now(), FinishedAt: time.Now()}}}
	if err := store.CommitDialogue(ctx, commit); err == nil {
		t.Fatal("expected statistics failure")
	}
	rows, err := store.ListSessionMessages(ctx, "s")
	if err != nil || len(rows) != 2 || rows[1].Content != "unknown" || rows[0].ToolBatchStatus != "pending" {
		t.Fatalf("partially committed result: %+v %v", rows, err)
	}
	if _, err := store.db.ExecContext(ctx, `DROP TRIGGER reject_tool_statistics`); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitDialogue(ctx, commit); err != nil {
		t.Fatal(err)
	}
	usage, err := store.UsageBySession(ctx, "s")
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
			initial.Native.Calls = []NativeCall{{ExchangeID: "exchange", CallID: "one", Name: "tool", Arguments: "{}", Status: "started"}, {ExchangeID: "exchange", CallID: "two", Name: "tool", Arguments: "{}", Status: "pending", Ordinal: 1}}
			if err := store.CommitDialogue(ctx, initial); err != nil {
				t.Fatal(err)
			}
			if fail {
				if _, err := store.db.ExecContext(ctx, `CREATE TRIGGER reject_snapshot BEFORE INSERT ON native_checkpoints BEGIN SELECT RAISE(ABORT,'snapshot failed'); END`); err != nil {
					t.Fatal(err)
				}
			}
			pair := &ToolBatch{
				Call:    &Message{ID: "head", SessionID: "s", Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "one", Name: "tool", Arguments: "{}"}}, Metadata: `{"tool_result_message_ids":["result"]}`},
				Results: []*Message{{ID: "result", SessionID: "s", Role: RoleTool, ToolCallID: "one", Content: "done"}},
			}
			call := initial.Native.Calls[0]
			call.Status, call.ResultInputID = "completed", "output"
			input := NativeInput{ID: "output", SessionID: "s", MessageID: "result", ExchangeID: "exchange", CallID: "one", ItemJSON: `{"type":"function_call_output","call_id":"one","output":"done"}`, MediaJSON: `[]`}
			commit := DialogueCommit{SessionID: "s", ToolBatch: pair, Native: &NativeCommit{ExpectedCheckpointID: "live", Calls: []NativeCall{call}, Inputs: []NativeInput{input}, Snapshot: &NativeCheckpoint{ID: "snapshot", SessionID: "s", ExchangeID: "exchange", ResponseID: "r", MessageID: "head"}}}
			err := store.CommitDialogue(ctx, commit)
			if (err != nil) != fail {
				t.Fatalf("snapshot error=%v", err)
			}
			current, err := store.CurrentCheckpoint(ctx, "s")
			if err != nil || current.ID != "live" || strings.Contains(current.CallsJSON, "completed") {
				t.Fatalf("live cursor changed=%+v %v", current, err)
			}
			rows, err := store.ListSessionMessages(ctx, "s")
			if err != nil {
				t.Fatal(err)
			}
			inputs, err := store.PendingInputs(ctx, "s")
			if err != nil {
				t.Fatal(err)
			}
			calls, err := store.Calls(ctx, "exchange")
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
			cp, err := store.CheckpointForMessage(ctx, "s", "head")
			if err != nil || cp.ID != "snapshot" || !strings.Contains(cp.CallsJSON, `"ResultInputID":"output"`) || !strings.Contains(cp.CallsJSON, `"Status":"pending"`) {
				t.Fatalf("snapshot=%+v %v", cp, err)
			}
			calls[1].Status = "started"
			if err := store.CommitDialogue(ctx, DialogueCommit{SessionID: "s", Native: &NativeCommit{ExpectedCheckpointID: "live", Calls: []NativeCall{calls[1]}}}); err != nil {
				t.Fatal(err)
			}
			frozen, err := store.GetCheckpoint(ctx, cp.ID)
			if err != nil || frozen.CallsJSON != cp.CallsJSON {
				t.Fatalf("historical snapshot changed=%+v %v", frozen, err)
			}
		})
	}
}
