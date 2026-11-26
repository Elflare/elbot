package branch_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"elbot/internal/agent/responses"
	"elbot/internal/llm"
	"elbot/internal/operationresult"
	"elbot/internal/session/branch"
	"elbot/internal/storage"
)

func TestCopyStoppedResponsesPersistsHistoryAndMedia(t *testing.T) {
	for _, mode := range []string{storage.SessionModeWork, storage.SessionModeBackground} {
		for _, stage := range []string{"first", "history"} {
			t.Run(mode+"/"+stage, func(t *testing.T) {
				f := newFixture(t)
				ctx := t.Context()
				source := f.source(t, mode)
				var err error
				source, err = f.store.MutateSession(ctx, source.ID, func(row *storage.Session) error {
					fields, err := storage.DecodeSessionMetadata(row.Metadata)
					if err != nil {
						return err
					}
					if err := fields.Set("responses_input_version", 2); err != nil {
						return err
					}
					row.Metadata, err = fields.Encode()
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
				checkpointID := ""
				if stage == "history" {
					old := &storage.Message{ID: "old-user", SessionID: source.ID, Role: storage.RoleUser, Content: "old question"}
					input := storage.NativeInput{ID: "old-input", SessionID: source.ID, MessageID: old.ID, ItemJSON: `{"type":"message","role":"user","content":[{"type":"input_text","text":"old question"}]}`, MediaJSON: `[]`}
					if err := f.store.CreateExchange(ctx, &storage.NativeExchange{Status: "pending", ID: "completed", SessionID: source.ID, APIType: "response", Provider: "p", BaseURL: "https://example.invalid", RequestJSON: `{"store":true}`, InputIDsJSON: `["old-input"]`}); err != nil {
						t.Fatal(err)
					}
					if err := f.store.FinishExchange(ctx, "completed", "completed", `{"id":"response","status":"completed","store":true}`, `[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"old answer"}]}]`, ""); err != nil {
						t.Fatal(err)
					}
					checkpointID = "checkpoint"
					commit := storage.DialogueCommit{SessionID: source.ID, Messages: []*storage.Message{old, {ID: "old-answer", SessionID: source.ID, Role: storage.RoleAssistant, Content: "old answer"}}, Native: &storage.NativeCommit{
						Inputs: []storage.NativeInput{input}, ConsumedInputs: []string{input.ID},
						Checkpoint: storage.NativeCheckpoint{ID: checkpointID, SessionID: source.ID, ExchangeID: "completed", ResponseID: "response", MessageID: "old-answer"},
					}}
					if err := f.store.CommitDialogue(ctx, commit); err != nil {
						t.Fatal(err)
					}
				}
				mediaID := "media:" + strings.Repeat("a", 64)
				if err := f.store.UpsertMedia(ctx, &storage.Media{ID: mediaID, Backend: "local", MIMEType: "image/png"}); err != nil {
					t.Fatal(err)
				}
				segments := []llm.MessageSegment{{Type: llm.SegmentText, Text: "unanswered question"}, {Type: llm.SegmentImage, MediaID: mediaID}}
				materialJSON, err := json.Marshal([]map[string]any{{"message_id": "stopped-user", "segments": segments}})
				if err != nil {
					t.Fatal(err)
				}
				message := &storage.Message{ID: "stopped-user", SessionID: source.ID, Role: storage.RoleUser, Content: "unanswered question", Segments: string(mustPendingJSON(t, segments))}
				input := storage.NativeInput{ID: "stopped-input", SessionID: source.ID, MessageID: message.ID, ItemJSON: `{"type":"message","role":"user","content":[{"type":"input_text","text":"unanswered question"},{"type":"input_image","image_url":""}]}`, MediaJSON: string(materialJSON)}
				if err := f.store.CommitDialogue(ctx, storage.DialogueCommit{SessionID: source.ID, Messages: []*storage.Message{message}, Native: &storage.NativeCommit{ExpectedCheckpointID: checkpointID, Inputs: []storage.NativeInput{input}}}); err != nil {
					t.Fatal(err)
				}
				// Model execution was stopped before any response checkpoint could be committed.
				if err := f.store.CreateExchange(ctx, &storage.NativeExchange{Status: "pending", ID: "stopped", SessionID: source.ID, APIType: "response", Provider: "p", PreviousCheckpointID: checkpointID, RequestJSON: `{"store":true}`, InputIDsJSON: `["stopped-input"]`}); err != nil {
					t.Fatal(err)
				}
				if err := f.store.FinishExchange(ctx, "stopped", "canceled", "", "", context.Canceled.Error()); err != nil {
					t.Fatal(err)
				}
				native := &responses.Context{Repository: f.store}
				f.material = func(ctx context.Context, row *storage.Session, _ *storage.Message) (*branch.PreparedMaterial, error) {
					prepared, err := native.PrepareCopy(ctx, row)
					if err != nil || prepared.Status != operationresult.Success {
						return nil, fmt.Errorf("prepare copy: %s: %w", prepared.Message, err)
					}
					return &branch.PreparedMaterial{Seed: prepared.Data.Seed, ExpectedCheckpointID: prepared.Data.ExpectedCheckpointID}, nil
				}
				target := scope()
				target.Platform, target.IsSuperadmin = "cli", true
				var copied operationresult.Result[*storage.Session]
				if mode == storage.SessionModeBackground {
					copied, err = f.s.CopyBackground(ctx, target, branch.BackgroundCopyRequest{SourceSessionID: source.ID, TaskType: "cron", TaskID: "copy"})
				} else {
					var created bool
					copied, created, err = f.s.CopyForeground(ctx, target, source.ID)
					if !created {
						t.Fatalf("foreground copy was not created: %+v %v", copied, err)
					}
				}
				if err != nil || copied.Status != operationresult.Success {
					t.Fatalf("copy stopped session: %+v %v", copied, err)
				}
				rows, err := f.store.ListSessionMessages(ctx, copied.Data.ID)
				wantMessages := 1
				if stage == "history" {
					wantMessages = 3
				}
				if err != nil || len(rows) != wantMessages || rows[len(rows)-1].ID == message.ID || rows[len(rows)-1].Content != message.Content {
					t.Fatalf("copied history: %+v %v", rows, err)
				}
				records, err := f.store.LoadNativeWindow(ctx, copied.Data, nil)
				if err != nil || records.Seed == nil || strings.Count(records.Seed.ItemsJSON, "unanswered question") != 1 {
					t.Fatalf("copied native material: %+v %v", records, err)
				}
				if _, err := native.Load(ctx, copied.Data, nil, records); err != nil {
					t.Fatal(err)
				}
				refs, err := f.store.ListByOwner(ctx, "native_seed", records.Seed.ID)
				if err != nil || len(refs) != 1 || refs[0].MediaID != mediaID {
					t.Fatalf("copied media references: %+v %v", refs, err)
				}
				pending, err := f.store.PendingInputs(ctx, source.ID)
				if err != nil || len(pending) != 1 || pending[0].ID != input.ID || pending[0].ConsumedBy != "" {
					t.Fatalf("source inputs changed: %+v %v", pending, err)
				}
			})
		}
	}
}

func mustPendingJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestCopyForegroundRechecksPendingInputs(t *testing.T) {
	for _, change := range []string{"added", "content"} {
		t.Run(change, func(t *testing.T) {
			f := newFixture(t)
			source := f.source(t, storage.SessionModeWork)
			input := storage.NativeInput{ID: "pending", SessionID: source.ID, ItemJSON: `{"type":"message","role":"user","content":[{"type":"input_text","text":"question"}]}`, MediaJSON: `[]`}
			if change != "added" {
				if change == "content" {
					input.ExchangeID, input.CallID = "exchange", "call"
					if err := f.store.CreateExchange(t.Context(), &storage.NativeExchange{Status: "pending", ID: "exchange", SessionID: source.ID, APIType: "response", Provider: "p", RequestJSON: `{}`}); err != nil {
						t.Fatal(err)
					}
				}
				commit := storage.NativeCommit{Inputs: []storage.NativeInput{input}}
				if change == "content" {
					commit.Calls = []storage.NativeCall{{ExchangeID: "exchange", CallID: "call", Name: "read", Arguments: `{}`, Status: "pending", ResultInputID: input.ID}}
				}
				if err := f.store.CommitDialogue(t.Context(), storage.DialogueCommit{SessionID: source.ID, Native: &commit}); err != nil {
					t.Fatal(err)
				}
			}
			f.material = func(ctx context.Context, _ *storage.Session, _ *storage.Message) (*branch.PreparedMaterial, error) {
				input.ItemJSON = `{"type":"message","role":"user","content":[{"type":"input_text","text":"changed"}]}`
				err := f.store.CommitDialogue(ctx, storage.DialogueCommit{SessionID: source.ID, Native: &storage.NativeCommit{Inputs: []storage.NativeInput{input}}})
				return &branch.PreparedMaterial{Seed: seed()}, err
			}
			target := scope()
			target.Platform, target.IsSuperadmin = "cli", true
			result, created, err := f.s.CopyForeground(t.Context(), target, source.ID)
			if err == nil && result.Status == operationresult.Success || created {
				t.Fatalf("accepted changed source: %+v %v", result, err)
			}
			if f.count(t) != 1 || f.c.CurrentBinding(target) != nil {
				t.Fatal("failed copy created or activated a session")
			}
		})
	}
}
