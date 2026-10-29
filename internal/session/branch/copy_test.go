package branch_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"elbot/internal/session/branch"
	"elbot/internal/session/control"
	"elbot/internal/storage"
)

func TestBackgroundCopyHistoryAndMetadata(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(fmt.Sprint(native), func(t *testing.T) {
			f := newFixture(t)
			if _, err := f.c.Create(t.Context(), scope(), control.CreateRequest{}); err != nil {
				t.Fatal(err)
			}
			current := f.c.CurrentBinding(scope())
			source := f.source(t, storage.SessionModeBackground)
			parent := f.message(t, source.ID, storage.RoleUser)
			answer := &storage.Message{SessionID: source.ID, Role: storage.RoleAssistant, Content: "answer", ParentMessageID: parent.ID, ReplyToMessageID: parent.ID, ReplyToPlatformMessageID: "platform"}
			if err := f.store.Messages().Append(t.Context(), answer); err != nil {
				t.Fatal(err)
			}
			batch := toolBatch(t, f, source.ID, "batch", false)
			mediaID := "media:" + strings.Repeat("a", 64)
			if native {
				if err := f.store.Media().Upsert(t.Context(), &storage.Media{ID: mediaID, Backend: "local", MIMEType: "image/png"}); err != nil {
					t.Fatal(err)
				}
				f.material = func(context.Context, *storage.Session, *storage.Message) (*branch.PreparedMaterial, error) {
					s := seed()
					s.MediaIDs = []string{mediaID}
					return &branch.PreparedMaterial{Seed: s}, nil
				}
			}
			target := scope()
			target.ActorID, target.Platform, target.PlatformScopeID = "recipient", "other", "cron:copy"
			metadata := storage.SessionMetadata{"unknown": json.RawMessage(`9007199254740993`), "nested": json.RawMessage(`{"keep":true}`), "title_source": json.RawMessage(`"wrong"`)}
			row, err := f.s.CopyBackground(t.Context(), target, branch.BackgroundCopyRequest{SourceSessionID: source.ID, Kind: "cron", Name: "copy", Metadata: metadata})
			if err != nil {
				t.Fatal(err)
			}
			if row.Mode != storage.SessionModeBackground || row.OwnerID != target.ActorID || row.Platform != target.Platform || row.Title != "Cron: copy" {
				t.Fatalf("copy: %+v", row)
			}
			for _, want := range []string{`"unknown":9007199254740993`, `"nested":{"keep":true}`, `"title_source":"cron"`, `"llm_origin"`} {
				if !strings.Contains(row.Metadata, want) {
					t.Fatalf("missing %s in %s", want, row.Metadata)
				}
			}
			if string(metadata["title_source"]) != `"wrong"` || strings.Contains(row.Metadata, "source_only") {
				t.Fatal("metadata ownership violated")
			}
			rows, err := f.store.Messages().ListBySession(t.Context(), row.ID)
			if err != nil || len(rows) != 5 {
				t.Fatalf("messages: %+v %v", rows, err)
			}
			if rows[0].ID == parent.ID || rows[1].ParentMessageID != "" || rows[1].ReplyToMessageID != "" || rows[1].ReplyToPlatformMessageID != "" {
				t.Fatal("source message refs retained")
			}
			ids, err := storage.ToolResultMessageIDs(rows[2])
			if err != nil {
				t.Fatal(err)
			}
			if len(ids) != 2 || ids[0] != rows[3].ID || ids[1] != rows[4].ID || ids[0] == batch.Results[0].ID || rows[3].ToolCallID != batch.Results[0].ToolCallID {
				t.Fatal("tool result links not remapped")
			}
			if f.c.CurrentBinding(scope()) != current || !current.Valid() {
				t.Fatal("copy changed current")
			}
			if native {
				records, err := f.store.Dialogues().LoadNativeWindow(t.Context(), row, nil)
				if err != nil || records.Seed == nil {
					t.Fatalf("seed: %+v %v", records, err)
				}
				refs, err := f.store.MediaReferences().ListByOwner(t.Context(), "native_seed", records.Seed.ID)
				if err != nil || len(refs) != 1 {
					t.Fatalf("seed media refs: %+v %v", refs, err)
				}
			}
		})
	}
}

func TestCopyRechecksAfterUnlockedPreparation(t *testing.T) {
	for _, change := range []string{"promote", "busy", "delete", "origin", "checkpoint", "pending-add", "pending-content", "cancel", "prepare-error", "save", "bad-seed"} {
		t.Run(change, func(t *testing.T) {
			f := newFixture(t)
			source := f.source(t, storage.SessionModeBackground)
			f.message(t, source.ID, storage.RoleAssistant)
			input := storage.NativeInput{ID: "input", SessionID: source.ID, ExchangeID: "exchange", CallID: "call", ItemJSON: `{"output":"pending"}`, MediaJSON: `[]`}
			if change == "pending-content" {
				if err := f.store.Dialogues().CreateExchange(t.Context(), &storage.NativeExchange{ID: "exchange", SessionID: source.ID, APIType: "response", Provider: "p", RequestJSON: `{}`}); err != nil {
					t.Fatal(err)
				}
				_, err := f.store.Dialogues().Commit(t.Context(), storage.DialogueCommit{SessionID: source.ID, Native: &storage.NativeCommit{Inputs: []storage.NativeInput{input}, Calls: []storage.NativeCall{{ExchangeID: "exchange", CallID: "call", Name: "tool", Arguments: `{}`, Status: "pending", ResultInputID: input.ID}}}})
				if err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if change == "save" {
				f.failSave()
			}
			wantCount := 1
			f.material = func(context.Context, *storage.Session, *storage.Message) (*branch.PreparedMaterial, error) {
				outside, stop := context.WithTimeout(context.Background(), time.Second)
				defer stop()
				locked, leave, err := f.c.EnterSessions(outside, source.ID)
				if err != nil {
					t.Fatal(err)
				}
				switch change {
				case "busy":
					f.setBusy(source.ID)
				case "delete":
					err = f.store.Sessions().Delete(locked, source.ID)
				case "origin", "checkpoint":
					_, err = f.store.Sessions().Mutate(locked, source.ID, func(row *storage.Session) error {
						if change == "origin" {
							row.Metadata = `{"llm_origin":{"protocol":"response","provider":"changed"}}`
						} else {
							row.Metadata = `{"llm_origin":{"protocol":"response","provider":"p"},"llm_checkpoint":"advanced"}`
						}
						return nil
					})
				case "pending-add", "pending-content":
					input.ItemJSON = `{"output":"completed"}`
					_, err = f.store.Dialogues().Commit(locked, storage.DialogueCommit{SessionID: source.ID, Native: &storage.NativeCommit{Inputs: []storage.NativeInput{input}}})
				case "cancel":
					cancel()
				}
				leave()
				if err != nil {
					t.Fatal(err)
				}
				if change == "promote" {
					if _, err := f.c.Resume(outside, scope(), source.ID); err != nil {
						t.Fatal(err)
					}
				}
				wantCount = f.count(t)
				if change == "prepare-error" {
					return &branch.PreparedMaterial{Seed: seed()}, fmt.Errorf("prepare failed")
				}
				prepared := &branch.PreparedMaterial{Seed: seed()}
				if change == "bad-seed" {
					prepared.Seed.ItemsJSON = "invalid"
				}
				return prepared, nil
			}
			if _, err := f.s.CopyBackground(ctx, scope(), branch.BackgroundCopyRequest{SourceSessionID: source.ID, Kind: "cron"}); err == nil {
				t.Fatal("invalid copy committed")
			}
			if f.count(t) != wantCount {
				t.Fatal("failed copy left session")
			}
			if change == "promote" {
				if f.c.CurrentBinding(scope()).SessionID() != source.ID {
					t.Fatal("copy overwrote promotion")
				}
			} else if f.c.CurrentBinding(scope()) != nil {
				t.Fatal("copy activated current")
			}
		})
	}
}

func TestCopyRejectsUnsealedAndMalformedHistory(t *testing.T) {
	for _, failure := range []string{"pending", "missing-result", "metadata", "foreground"} {
		t.Run(failure, func(t *testing.T) {
			f := newFixture(t)
			mode := storage.SessionModeBackground
			if failure == "foreground" {
				mode = storage.SessionModeWork
			}
			source := f.source(t, mode)
			req := branch.BackgroundCopyRequest{SourceSessionID: source.ID, Kind: "cron"}
			switch failure {
			case "pending":
				toolBatch(t, f, source.ID, "unfinished", true)
			case "missing-result":
				message := &storage.Message{SessionID: source.ID, Role: storage.RoleAssistant, Metadata: `{"tool_result_message_ids":["absent"]}`}
				if err := f.store.Messages().Append(t.Context(), message); err != nil {
					t.Fatal(err)
				}
			case "metadata":
				req.Metadata = storage.SessionMetadata{"bad": json.RawMessage(`invalid`)}
			}
			before := f.c.CurrentBinding(scope())
			if _, err := f.s.CopyBackground(t.Context(), scope(), req); err == nil {
				t.Fatal("invalid copy accepted")
			}
			if f.count(t) != 1 || f.c.CurrentBinding(scope()) != before {
				t.Fatal("failed copy changed state")
			}
		})
	}
}
