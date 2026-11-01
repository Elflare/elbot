package session

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/session/share"
	"elbot/internal/storage"
)

func TestSessionIDFormatsShareOneSequence(t *testing.T) {
	svc, _ := newTestService(t)
	cases := []struct {
		platform   string
		kind       contextinfo.ConversationKind
		background string
		targets    []string
		prefix     string
	}{
		{"qqonebot", contextinfo.ConversationPrivate, "", nil, "ob"},
		{"qqonebot", contextinfo.ConversationGroup, "", nil, "OB"},
		{"qqofficial", contextinfo.ConversationPrivate, "", nil, "qq"},
		{"qqofficial", contextinfo.ConversationGroup, "", nil, "QQ"},
		{"telegram", contextinfo.ConversationPrivate, "", nil, "tg"},
		{"telegram", contextinfo.ConversationChannel, "", nil, "TG"},
		{"cli", contextinfo.ConversationGroup, "", nil, "cl"},
		{"cli", "", "cron", []string{"qqonebot"}, "crob"},
		{"cli", "", "elnis", []string{"telegram", "telegram"}, "ewtg"},
		{"cli", "", "cron", []string{"telegram", "qqonebot"}, "crmx"},
		{"telegram", "", "elnis", []string{"qqofficial", "telegram"}, "ewmx"},
	}
	for i, tc := range cases {
		t.Run(tc.prefix, func(t *testing.T) {
			scope := Scope{ActorID: "owner", Platform: tc.platform, PlatformScopeID: fmt.Sprint(i), ConversationKind: tc.kind}
			var row *storage.Session
			var err error
			if tc.background == "" {
				row, err = svc.Create(t.Context(), scope, CreateRequest{})
			} else {
				row, err = svc.PrepareBackground(t.Context(), scope, BackgroundRequest{Kind: tc.background, Name: "task", TargetPlatforms: tc.targets})
			}
			if err != nil {
				t.Fatal(err)
			}
			if want := fmt.Sprintf("%s%d", tc.prefix, i+1); row.ID != want {
				t.Fatalf("ID=%q want %q", row.ID, want)
			}
			origin, err := share.SessionIDOrigin(row, share.Scope{})
			if err != nil {
				t.Fatal(err)
			}
			prefix, err := origin.Prefix()
			if err != nil || prefix != tc.prefix {
				t.Fatalf("saved origin=%+v, prefix=%q error=%v", origin, prefix, err)
			}
		})
	}
}

func TestSessionIDResetReservationAndCompaction(t *testing.T) {
	svc, store := newTestService(t)
	scope := Scope{ActorID: "owner", Platform: "telegram", PlatformScopeID: "group", ConversationKind: contextinfo.ConversationGroup}
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
	reserved, err := svc.AllocateID(t.Context(), scope, row)
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
	nextID, err := svc.AllocateID(t.Context(), scope, row)
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
	if _, err := share.SessionIDOrigin(next, share.Scope{}); err != nil {
		t.Fatal(err)
	}
}

func TestSessionIDForkCopyAndLegacySources(t *testing.T) {
	svc, store := newTestService(t)
	scope := Scope{ActorID: "owner", Platform: "qqonebot", PlatformScopeID: "private", ConversationKind: contextinfo.ConversationPrivate}
	legacy := &storage.Session{ID: storage.NewID(), OwnerID: scope.ActorID, Platform: scope.Platform, PlatformScopeID: scope.PlatformScopeID}
	if err := store.Sessions().Create(t.Context(), legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Resume(t.Context(), scope, legacy.ID); err != nil {
		t.Fatal(err)
	}
	message := &storage.Message{ID: storage.NewID(), SessionID: legacy.ID, Role: storage.RoleAssistant, Content: "answer"}
	if err := store.Messages().Append(t.Context(), message); err != nil {
		t.Fatal(err)
	}
	fork, err := svc.Fork(t.Context(), scope, message.ID)
	if err != nil || fork.ID != "ob1" {
		t.Fatalf("fork=%+v err=%v", fork, err)
	}
	if _, err := store.Sessions().Get(t.Context(), legacy.ID); err != nil {
		t.Fatal(err)
	}
	background, err := svc.PrepareBackground(t.Context(), scope, BackgroundRequest{Kind: "cron", Name: "task", TargetPlatforms: []string{"telegram", "qqonebot"}})
	if err != nil {
		t.Fatal(err)
	}
	if background.ID != "crmx2" {
		t.Fatal(background.ID)
	}
	reserved, err := svc.AllocateID(t.Context(), scope, background)
	if err != nil || reserved != "crmx3" {
		t.Fatalf("background reserve=%q err=%v", reserved, err)
	}
	copyScope := Scope{ActorID: "owner", Platform: "telegram", PlatformScopeID: "cron:task"}
	copy, err := svc.CopyBackground(t.Context(), copyScope, BackgroundCopyRequest{SourceSessionID: background.ID, Kind: "cron", Name: "task"})
	if err != nil || copy.ID != "crtg4" {
		t.Fatalf("copy=%+v err=%v", copy, err)
	}
	promoted, err := svc.Resume(t.Context(), scope, background.ID)
	if err != nil || promoted.ID != background.ID || promoted.Mode != storage.SessionModeWork {
		t.Fatalf("promotion changed ID: %+v %v", promoted, err)
	}
	reserved, err = svc.AllocateID(t.Context(), scope, promoted)
	if err != nil || reserved != "crmx5" {
		t.Fatalf("promotion changed creation origin: %q %v", reserved, err)
	}
}
