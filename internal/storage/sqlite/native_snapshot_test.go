package sqlite

import (
	"strings"
	"testing"

	"elbot/internal/storage"
)

func TestNativeMessageSnapshotCommitsWithPairWithoutAdvancingCursor(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "rollback"}[fail], func(t *testing.T) {
			store, _ := nativeStorageFixture(t)
			ctx := t.Context()
			initial := nativeStorageCommit("live", "")
			initial.Messages = nil
			initial.Native.Checkpoint.MessageID = ""
			initial.Native.Calls = []storage.NativeCall{{ExchangeID: "exchange", CallID: "one", Name: "tool", Arguments: "{}", Status: "started"}, {ExchangeID: "exchange", CallID: "two", Name: "tool", Arguments: "{}", Status: "pending", Ordinal: 1}}
			if err := store.Dialogues().Commit(ctx, initial); err != nil {
				t.Fatal(err)
			}
			if fail {
				if _, err := store.db.ExecContext(ctx, `CREATE TRIGGER reject_snapshot BEFORE INSERT ON native_checkpoints BEGIN SELECT RAISE(ABORT,'snapshot failed'); END`); err != nil {
					t.Fatal(err)
				}
			}
			pair := &storage.ToolPair{
				Call:   &storage.Message{ID: "head", SessionID: "s", Role: storage.RoleAssistant, Metadata: `{"tool_calls":[{"ID":"one","Name":"tool","Arguments":"{}"}],"tool_result_message_id":"result"}`},
				Result: &storage.Message{ID: "result", SessionID: "s", Role: storage.RoleTool, ToolCallID: "one", Content: "done"},
			}
			call := initial.Native.Calls[0]
			call.Status, call.ResultInputID = "completed", "output"
			input := storage.NativeInput{ID: "output", SessionID: "s", MessageID: "result", ExchangeID: "exchange", CallID: "one", ItemJSON: `{"type":"function_call_output","call_id":"one","output":"done"}`, MediaJSON: `[]`}
			commit := storage.DialogueCommit{SessionID: "s", ToolPair: pair, Native: &storage.NativeCommit{ExpectedCheckpointID: "live", Calls: []storage.NativeCall{call}, Inputs: []storage.NativeInput{input}, Snapshot: &storage.NativeCheckpoint{ID: "snapshot", SessionID: "s", ExchangeID: "exchange", ResponseID: "r", MessageID: "head"}}}
			err := store.Dialogues().Commit(ctx, commit)
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
			if err := store.Dialogues().Commit(ctx, storage.DialogueCommit{SessionID: "s", Native: &storage.NativeCommit{ExpectedCheckpointID: "live", Calls: []storage.NativeCall{calls[1]}}}); err != nil {
				t.Fatal(err)
			}
			frozen, err := store.Dialogues().GetCheckpoint(ctx, cp.ID)
			if err != nil || frozen.CallsJSON != cp.CallsJSON {
				t.Fatalf("historical snapshot changed=%+v %v", frozen, err)
			}
		})
	}
}
