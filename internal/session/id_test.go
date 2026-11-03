package session

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/session/share"
	"elbot/internal/storage"
)

func TestSessionIDFormatsShareOneSequence(t *testing.T) {
	svc, _ := newTestService(t)
	cases := []struct {
		platform   string
		kind       contextinfo.ChatType
		background string
		targets    []string
		prefix     string
	}{
		{"qqonebot", contextinfo.ChatPrivate, "", nil, "ob"},
		{"qqonebot", contextinfo.ChatGroup, "", nil, "OB"},
		{"qqofficial", contextinfo.ChatPrivate, "", nil, "qq"},
		{"qqofficial", contextinfo.ChatGroup, "", nil, "QQ"},
		{"telegram", contextinfo.ChatPrivate, "", nil, "tg"},
		{"telegram", contextinfo.ChatChannel, "", nil, "TG"},
		{"cli", contextinfo.ChatPrivate, "", nil, "cl"},
		{"cli", contextinfo.ChatGroup, "", nil, "cl"},
		{"qqonebot", contextinfo.ChatGroup, "cron", nil, "crob"},
		{"cli", "", "cron", []string{"qqonebot"}, "crob"},
		{"cli", "", "elnis", []string{"telegram", "telegram"}, "ewtg"},
		{"cli", "", "cron", []string{"telegram", "qqonebot"}, "crmx"},
		{"telegram", "", "elnis", []string{"qqofficial", "telegram"}, "ewmx"},
	}
	for i, tc := range cases {
		t.Run(tc.prefix, func(t *testing.T) {
			scope := Scope{Owner: contextinfo.ActorIdentity{Platform: "", ID: "owner"}, Platform: tc.platform, ChatID: fmt.Sprint(i), ChatType: tc.kind}
			var row *storage.Session
			var err error
			if tc.background == "" {
				row, err = svc.Create(t.Context(), scope, CreateRequest{})
			} else {
				row, err = svc.PrepareBackground(t.Context(), scope, BackgroundRequest{TaskType: tc.background, TaskID: "task", TargetPlatforms: tc.targets})
			}
			if err != nil {
				t.Fatal(err)
			}
			if want := fmt.Sprintf("%s%d", tc.prefix, i+1); row.ID != want {
				t.Fatalf("ID=%q want %q", row.ID, want)
			}
			origin, err := share.SessionIDOrigin(row)
			if err != nil {
				t.Fatal(err)
			}
			prefix, err := origin.Prefix(svc.sessionIDCode)
			if err != nil || prefix != tc.prefix {
				t.Fatalf("saved origin=%+v, prefix=%q error=%v", origin, prefix, err)
			}
		})
	}
}

func TestSessionIDResetReservationAndCompaction(t *testing.T) {
	svc, store := newTestService(t)
	scope := Scope{Owner: contextinfo.ActorIdentity{Platform: "telegram", ID: "owner"}, Platform: "telegram", ChatID: "group", ChatType: contextinfo.ChatGroup}
	for range 3 {
		if err := svc.ResetCurrent(t.Context(), scope); err != nil {
			t.Fatal(err)
		}
	}
	row, err := svc.GetOrCreateCurrent(t.Context(), scope, "first")
	if err != nil {
		t.Fatal(err)
	}
	if row.ID != "TG1" {
		t.Fatalf("/new consumed numbers: %q", row.ID)
	}
	reserved, err := svc.AllocateID(t.Context(), row)
	if err != nil || reserved != "TG2" {
		t.Fatalf("reserved=%q err=%v", reserved, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := svc.CreateCompacted(ctx, scope, row.ID, CompactedRequest{ID: reserved}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled compact: %v", err)
	}
	if _, err := store.Sessions().Get(t.Context(), reserved); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("unused reservation was saved: %v", err)
	}
	nextID, err := svc.AllocateID(t.Context(), row)
	if err != nil || nextID != "TG3" {
		t.Fatalf("reservation reused: %q %v", nextID, err)
	}
	next, err := svc.CreateCompacted(t.Context(), scope, row.ID, CompactedRequest{ID: nextID, Metadata: `{"unknown":9007199254740993}`})
	if err != nil {
		t.Fatal(err)
	}
	if next.ID != "TG3" {
		t.Fatal(next.ID)
	}
	if _, err := share.SessionIDOrigin(next); err != nil {
		t.Fatal(err)
	}
}

func TestSessionIDForkCopyAndPromotion(t *testing.T) {
	svc, store := newTestService(t)
	scope := Scope{Owner: contextinfo.ActorIdentity{Platform: "qqonebot", ID: "owner"}, Platform: "qqonebot", ChatID: "private", ChatType: contextinfo.ChatPrivate}
	source, err := svc.Create(t.Context(), scope, CreateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	message := &storage.Message{ID: storage.NewID(), SessionID: source.ID, Role: storage.RoleAssistant, Content: "answer"}
	if err := store.Messages().Append(t.Context(), message); err != nil {
		t.Fatal(err)
	}
	fork, err := svc.Fork(t.Context(), scope, message.ID)
	if err != nil || fork.ID != "ob2" {
		t.Fatalf("fork=%+v err=%v", fork, err)
	}
	if _, err := store.Sessions().Get(t.Context(), source.ID); err != nil {
		t.Fatal(err)
	}
	background, err := svc.PrepareBackground(t.Context(), scope, BackgroundRequest{TaskType: "cron", TaskID: "task", TargetPlatforms: []string{"telegram", "qqonebot"}})
	if err != nil {
		t.Fatal(err)
	}
	if background.ID != "crmx3" {
		t.Fatal(background.ID)
	}
	reserved, err := svc.AllocateID(t.Context(), background)
	if err != nil || reserved != "crmx4" {
		t.Fatalf("background reserve=%q err=%v", reserved, err)
	}
	copyScope := Scope{Owner: contextinfo.ActorIdentity{Platform: "telegram", ID: "owner"}, Platform: "telegram", ChatType: "cron", ChatID: "task"}
	copy, err := svc.CopyBackground(t.Context(), copyScope, BackgroundCopyRequest{SourceSessionID: background.ID, TaskType: "cron", TaskID: "task"})
	if err != nil || copy.ID != "crtg5" {
		t.Fatalf("copy=%+v err=%v", copy, err)
	}
	promoted, err := svc.Resume(t.Context(), scope, background.ID)
	if err != nil || promoted.ID != background.ID || promoted.Mode != storage.SessionModeWork {
		t.Fatalf("promotion changed ID: %+v %v", promoted, err)
	}
	reserved, err = svc.AllocateID(t.Context(), promoted)
	if err != nil || reserved != "crmx6" {
		t.Fatalf("promotion changed creation origin: %q %v", reserved, err)
	}
}

func TestSessionIDQueryFailureDoesNotConsumeSequence(t *testing.T) {
	store := newTestStore(t)
	fault := errors.New("platform is not registered")
	svc := newTestSupervisor(t, store, func(_ *config.Config, deps *SupervisorDeps) {
		deps.SessionIDCode = func(name string, _ contextinfo.ChatType) (string, error) {
			if name != "future" {
				return "", fault
			}
			return "fx", nil
		}
	})
	scope := Scope{Owner: contextinfo.ActorIdentity{Platform: "unknown", ID: "owner"}, Platform: "unknown", ChatID: "private"}
	if _, err := svc.Create(t.Context(), scope, CreateRequest{}); !errors.Is(err, fault) {
		t.Fatalf("unknown platform=%v", err)
	}
	scope.Platform = "future"
	row, err := svc.Create(t.Context(), scope, CreateRequest{})
	if err != nil || row.ID != "fx1" {
		t.Fatalf("sequence consumed or code changed: row=%+v error=%v", row, err)
	}
}

func TestSessionIDMissingOriginDoesNotConsumeSequence(t *testing.T) {
	svc, _ := newTestService(t)
	for _, metadata := range []string{"", `{}`, `{"background_kind":"cron"}`, `{"id_origin":null}`, `{"id_origin":{}}`, `{"id_origin":"invalid"}`} {
		source := &storage.Session{Platform: "cli", Metadata: metadata}
		if _, err := svc.AllocateID(t.Context(), source); err == nil {
			t.Fatalf("accepted invalid origin: %s", metadata)
		}
	}
	row, err := svc.Create(t.Context(), Scope{Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "owner"}, Platform: "cli", ChatID: "local", ChatType: contextinfo.ChatPrivate}, CreateRequest{})
	if err != nil || row.ID != "cl1" {
		t.Fatalf("failed reservations consumed sequence: row=%+v error=%v", row, err)
	}
}
