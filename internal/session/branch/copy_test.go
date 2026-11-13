package branch_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/operationresult"
	"elbot/internal/session/branch"
	"elbot/internal/session/control"
	"elbot/internal/storage"
)

func TestBackgroundCopyHistoryAndMetadata(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(fmt.Sprint(native), func(t *testing.T) {
			f := newFixture(t)
			if result, err := f.c.Create(t.Context(), scope(), control.CreateRequest{}); err != nil || result.Status != operationresult.Success {
				t.Fatalf("create: %+v %v", result, err)
			}
			current := f.c.CurrentBinding(scope())
			source := f.source(t, storage.SessionModeBackground)
			parent := f.message(t, source.ID, storage.RoleUser)
			answer := &storage.Message{ID: storage.NewID(), SessionID: source.ID, Role: storage.RoleAssistant, Content: "answer", ParentMessageID: parent.ID, ReplyToMessageID: parent.ID, ReplyToPlatformMessageID: "platform"}
			if err := f.store.Messages().Append(t.Context(), answer); err != nil {
				t.Fatal(err)
			}
			batch := toolBatch(t, f, source.ID, "batch", false)
			for _, record := range []storage.ToolCallRecord{
				{SessionID: source.ID, ToolCallID: batch.Call.ToolCalls[0].ID, ToolName: "tool", Success: true},
				{SessionID: source.ID, ToolCallID: batch.Call.ToolCalls[1].ID, ToolName: "tool", Success: false, Error: "failed"},
			} {
				if err := f.store.ToolCalls().Create(t.Context(), &record); err != nil {
					t.Fatal(err)
				}
			}
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
			target.Owner.ID, target.Platform, target.ChatID = "recipient", "other", "cron:copy"
			metadata := storage.SessionMetadata{"unknown": json.RawMessage(`9007199254740993`), "nested": json.RawMessage(`{"keep":true}`), "title_source": json.RawMessage(`"wrong"`)}
			copied, err := f.s.CopyBackground(t.Context(), target, branch.BackgroundCopyRequest{SourceSessionID: source.ID, TaskType: "cron", TaskID: "copy", Metadata: metadata})
			if err != nil || copied.Status != operationresult.Success {
				t.Fatalf("copy: %+v %v", copied, err)
			}
			row := copied.Data
			if row.Mode != storage.SessionModeBackground || row.Owner != target.Owner || row.Platform != target.Platform || row.Title != "Cron: copy" {
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
			records, err := f.store.ToolCalls().ListBySession(t.Context(), row.ID)
			if err != nil || len(records) != 2 || records[0].SessionID != row.ID || records[1].SessionID != row.ID || records[0].ToolCallID != batch.Call.ToolCalls[0].ID || records[0].Success != true || records[1].ToolCallID != batch.Call.ToolCalls[1].ID || records[1].Success || records[1].Error != "failed" {
				t.Fatalf("copied tool call records: %+v %v", records, err)
			}
			originalRecords, err := f.store.ToolCalls().ListBySession(t.Context(), source.ID)
			if err != nil || len(originalRecords) != 2 || originalRecords[0].SessionID != source.ID || originalRecords[1].SessionID != source.ID {
				t.Fatalf("source tool call records changed: %+v %v", originalRecords, err)
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

func TestCopyForegroundAcrossPlatformsPreservesSourceAndHistory(t *testing.T) {
	f := newFixture(t)
	source := f.source(t, storage.SessionModeWork)
	first := f.message(t, source.ID, storage.RoleUser)
	second := &storage.Message{ID: storage.NewID(), SessionID: source.ID, Role: storage.RoleAssistant, Content: "answer", ParentMessageID: first.ID}
	if err := f.store.Messages().Append(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	target := scope()
	target.Owner = contextinfo.ActorIdentity{Platform: "cli", ID: "admin"}
	target.Platform, target.ChatID, target.IsSuperadmin = "cli", "local", true
	copyResult, created, err := f.s.CopyForeground(t.Context(), target, source.ID)
	if err != nil || copyResult.Status != operationresult.Success || !created {
		t.Fatalf("copy: %+v %v", copyResult, err)
	}
	copy := copyResult.Data
	if copy.ID == source.ID || copy.ParentSessionID != source.ID || copy.Owner != target.Owner || copy.Platform != target.Platform || copy.ChatID != target.ChatID {
		t.Fatalf("copy identity: %+v", copy)
	}
	copiedMessages, err := f.store.Messages().ListBySession(t.Context(), copy.ID)
	if err != nil || len(copiedMessages) != 2 {
		t.Fatalf("copied messages: %+v %v", copiedMessages, err)
	}
	if copiedMessages[0].ID == first.ID || copiedMessages[1].ID == second.ID || copiedMessages[1].ParentMessageID != "" {
		t.Fatalf("message references were not copied independently: %+v", copiedMessages)
	}
	storedSource, err := f.store.Sessions().Get(t.Context(), source.ID)
	if err != nil || storedSource.Platform != "test" {
		t.Fatalf("source changed: %+v %v", storedSource, err)
	}
	if current := f.c.CurrentBinding(target); current == nil || current.SessionID() != copy.ID {
		t.Fatal("copy was not activated for target scope")
	}
}

func TestCopyForegroundDoesNotReplaceBusyCurrentSession(t *testing.T) {
	f := newFixture(t)
	target := scope()
	target.Owner = contextinfo.ActorIdentity{Platform: "cli", ID: "admin"}
	target.Platform, target.ChatID, target.IsSuperadmin = "cli", "local", true
	currentResult, err := f.c.Create(t.Context(), target, control.CreateRequest{Title: "current"})
	if err != nil || currentResult.Status != operationresult.Success {
		t.Fatalf("create current: %+v %v", currentResult, err)
	}
	current := currentResult.Data
	source := f.source(t, storage.SessionModeWork)
	f.setBusy(current.ID)
	if result, created, err := f.s.CopyForeground(t.Context(), target, source.ID); err != nil || result.Status != operationresult.Busy || created {
		t.Fatalf("copy replaced busy current session: %+v %v", result, err)
	}
	if f.count(t) != 2 {
		t.Fatal("copy created a session despite busy current binding")
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
				err := f.store.Dialogues().Commit(t.Context(), storage.DialogueCommit{SessionID: source.ID, Native: &storage.NativeCommit{Inputs: []storage.NativeInput{input}, Calls: []storage.NativeCall{{ExchangeID: "exchange", CallID: "call", Name: "tool", Arguments: `{}`, Status: "pending", ResultInputID: input.ID}}}})
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
					err = f.store.Dialogues().Commit(locked, storage.DialogueCommit{SessionID: source.ID, Native: &storage.NativeCommit{Inputs: []storage.NativeInput{input}}})
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
			if result, err := f.s.CopyBackground(ctx, scope(), branch.BackgroundCopyRequest{SourceSessionID: source.ID, TaskType: "cron"}); err == nil && result.Status == operationresult.Success {
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
	for _, failure := range []string{"pending", "missing-result", "non-tool-result", "metadata", "foreground"} {
		t.Run(failure, func(t *testing.T) {
			f := newFixture(t)
			mode := storage.SessionModeBackground
			if failure == "foreground" {
				mode = storage.SessionModeWork
			}
			source := f.source(t, mode)
			req := branch.BackgroundCopyRequest{SourceSessionID: source.ID, TaskType: "cron"}
			wantStatus, wantError := operationresult.Conflict, false
			switch failure {
			case "pending":
				toolBatch(t, f, source.ID, "unfinished", true)
			case "missing-result", "non-tool-result":
				resultID := "absent"
				if failure == "non-tool-result" {
					resultID = f.message(t, source.ID, storage.RoleAssistant).ID
				}
				message := &storage.Message{ID: storage.NewID(), SessionID: source.ID, Role: storage.RoleAssistant, Metadata: `{"tool_result_message_ids":["` + resultID + `"]}`}
				if err := f.store.Messages().Append(t.Context(), message); err != nil {
					t.Fatal(err)
				}
				wantError = true
			case "metadata":
				req.Metadata = storage.SessionMetadata{"bad": json.RawMessage(`invalid`)}
				wantError = true
			}
			before := f.c.CurrentBinding(scope())
			result, err := f.s.CopyBackground(t.Context(), scope(), req)
			if (wantError && err == nil) || (!wantError && (err != nil || result.Status != wantStatus)) {
				t.Fatalf("copy result: %+v %v", result, err)
			}
			if f.count(t) != 1 || f.c.CurrentBinding(scope()) != before {
				t.Fatal("failed copy changed state")
			}
		})
	}
}
