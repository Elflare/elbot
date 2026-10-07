package session

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"elbot/internal/llm"
	"elbot/internal/storage"
)

func TestCopyBackgroundKeepsForegroundBindingsAndMetadataTypes(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	scope := Scope{ActorID: "cli:local", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	current, err := svc.Create(ctx, scope, CreateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	_, original, err := svc.CurrentBound(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	source, err := svc.PrepareBackground(ctx, scope, BackgroundRequest{Kind: "cron", Name: "job"})
	if err != nil {
		t.Fatal(err)
	}
	origin := llm.Origin{APIType: llm.APITypeChat, Provider: "source", BaseURL: "https://source.invalid/v1"}
	if _, err := svc.RegisterOrigin(ctx, source.ID, origin); err != nil {
		t.Fatal(err)
	}
	parent := &storage.Message{SessionID: source.ID, Role: storage.RoleUser, Content: "question"}
	if err := store.Messages().Append(ctx, parent); err != nil {
		t.Fatal(err)
	}
	if err := store.Messages().Append(ctx, &storage.Message{SessionID: source.ID, Role: storage.RoleAssistant, Content: "answer", ParentMessageID: parent.ID, ReplyToMessageID: parent.ID, ReplyToPlatformMessageID: "platform-reply"}); err != nil {
		t.Fatal(err)
	}
	for _, target := range []Scope{scope, {ActorID: "qq:2", Platform: "qq", PlatformScopeID: "cron:job"}} {
		metadata := storage.SessionMetadata{"cron_broadcast_copy": json.RawMessage(`true`), "unknown": json.RawMessage(`9007199254740993`), "nested": json.RawMessage(`{"keep":true}`), "title_source": json.RawMessage(`"wrong"`)}
		copy, err := svc.CopyBackground(ctx, target, BackgroundCopyRequest{SourceSessionID: source.ID, Kind: "cron", Name: "job", Title: "report", Metadata: metadata})
		if err != nil {
			t.Fatal(err)
		}
		if copy.Mode != storage.SessionModeBackground || copy.OwnerID != target.ActorID || copy.Platform != target.Platform || copy.PlatformScopeID != target.PlatformScopeID || copy.Title != "report" || !IsBackground(copy) {
			t.Fatalf("copy=%#v", copy)
		}
		if got, present, err := Origin(copy); err != nil || !present || got != origin {
			t.Fatalf("origin=%+v err=%v", got, err)
		}
		fields, err := storage.DecodeSessionMetadata(copy.Metadata)
		if err != nil || string(fields["cron_broadcast_copy"]) != "true" || string(fields["unknown"]) != "9007199254740993" || string(fields["title_source"]) != `"cron"` || string(fields["background_kind"]) != `"cron"` {
			t.Fatalf("metadata=%s/%v", copy.Metadata, err)
		}
		if string(metadata["title_source"]) != `"wrong"` {
			t.Fatal("mutated caller metadata")
		}
		messages, err := store.Messages().ListBySession(ctx, copy.ID)
		if err != nil || len(messages) != 2 {
			t.Fatalf("history=%#v/%v", messages, err)
		}
		if messages[0].ID == parent.ID || messages[1].ParentMessageID != "" || messages[1].ReplyToMessageID != "" || messages[1].ReplyToPlatformMessageID != "" {
			t.Fatalf("copied source references: %#v", messages)
		}
	}
	latest, binding, err := svc.CurrentBound(ctx, scope)
	if err != nil || latest.ID != current.ID || original != binding || !binding.Valid() {
		t.Fatalf("changed current: %#v/%v", latest, err)
	}
}

func TestCopyBackgroundRejectsPromotionCancellationAndSaveFailure(t *testing.T) {
	for _, mode := range []string{"promotion", "cancel", "save", "missing", "metadata"} {
		t.Run(mode, func(t *testing.T) {
			svc, store := newTestService(t)
			ctx := context.Background()
			scope := Scope{ActorID: "cli:local", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
			source, err := svc.PrepareBackground(ctx, scope, BackgroundRequest{Kind: "cron", Name: "job"})
			if err != nil {
				t.Fatal(err)
			}
			req := BackgroundCopyRequest{SourceSessionID: source.ID, Kind: "cron", Name: "job"}
			switch mode {
			case "promotion":
				_, err = svc.Resume(ctx, scope, source.ID)
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "save":
				svc.store = storeWithSessionRepository{Store: store, sessions: compactFailCreateRepo{SessionRepository: store.Sessions(), err: errors.New("save failed")}}
			case "missing":
				req.SourceSessionID = "missing"
			case "metadata":
				req.Metadata = storage.SessionMetadata{"bad": json.RawMessage(`invalid`)}
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svc.CopyBackground(ctx, scope, req); err == nil {
				t.Fatal("expected failure")
			} else if mode == "promotion" && !errors.Is(err, ErrForegroundSession) {
				t.Fatal(err)
			}
			rows, err := store.Sessions().List(context.Background(), storage.ListSessionsRequest{IncludeAllPlatforms: true, Limit: 10})
			if err != nil || len(rows) != 1 {
				t.Fatalf("created failed copy: %#v/%v", rows, err)
			}
		})
	}
}
