package refcontext

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"elbot/internal/media"
	"elbot/internal/platform"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

func TestReferenceMediaOutputHistoryAndFallbackPriority(t *testing.T) {
	ctx := context.Background()
	store := newRefTestStore(t)
	root := t.TempDir()
	center := media.NewManager(store, root, &media.LocalBackend{Root: root})
	item, err := center.ImportBytes(ctx, []byte("image"), media.Input{Name: "image.png", MIMEType: "image/png"})
	if err != nil {
		t.Fatal(err)
	}
	history, err := sqlite.NewChatHistory(ctx, filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer history.Close()
	raw := []platform.MessageSegment{{Type: platform.SegmentImage, PlatformFileID: "raw-image"}, {Type: platform.SegmentFile, PlatformFileID: "raw-file"}, {Type: platform.SegmentImage, PlatformFileID: "raw-image"}}
	for _, id := range []string{"output", "history"} {
		if err := history.Repository().Append(ctx, &storage.ChatMessage{Platform: "telegram", PlatformScopeID: "group:9", PlatformMessageID: id, SenderID: "other", Segments: platform.MarshalChatSegments(raw)}); err != nil {
			t.Fatal(err)
		}
	}
	for i, kind := range []string{"image", "file", "image"} {
		if err := store.Media().SaveOutput(ctx, storage.MediaOutput{Platform: "telegram", ScopeID: "group:9", MessageID: "output", SegmentIndex: i, Kind: kind, MediaID: item.ID, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	opts := Options{Store: store, ChatHistory: history.Repository(), Platform: "telegram", ScopeID: "group:9", Text: "看看", Fetch: func(context.Context, string) (ReferencedMessage, bool) {
		calls++
		return ReferencedMessage{Segments: raw}, true
	}}
	opts.ReplyID = "output"
	got := Apply(ctx, opts)
	if calls != 0 || len(got.ReferenceSegments) != 3 || got.ReferenceSegments[0].MediaID != item.ID || got.ReferenceSegments[1].Type != platform.SegmentFile || got.ReferenceSegments[2].MediaID != item.ID {
		t.Fatalf("output priority = %#v, calls=%d", got, calls)
	}
	opts.ReplyID = "history"
	got = Apply(ctx, opts)
	if calls != 0 || len(got.ReferenceSegments) != 3 || got.ReferenceSegments[0].PlatformFileID != "raw-image" || got.ReferenceSegments[1].PlatformFileID != "raw-file" || got.ReferenceSegments[2] != got.ReferenceSegments[0] {
		t.Fatalf("history order = %#v, calls=%d", got, calls)
	}
	opts.ReplyID = "missing"
	got = Apply(ctx, opts)
	if calls != 1 || len(got.ReferenceSegments) != 3 {
		t.Fatalf("platform fallback = %#v, calls=%d", got, calls)
	}
}

func TestLatestCurrentAssistantSkipsMediaButOtherSessionRestores(t *testing.T) {
	ctx := context.Background()
	store := newRefTestStore(t)
	scope := session.Scope{ActorID: "telegram:1", Platform: "telegram", PlatformScopeID: "group:9"}
	first, latest := createAssistantMessages(t, ctx, store, scope)
	mapPlatformMessage(t, ctx, store, scope, "old", first)
	mapPlatformMessage(t, ctx, store, scope, "latest", latest)
	calls := 0
	opts := Options{Store: store, Platform: scope.Platform, ScopeID: scope.PlatformScopeID, ActorID: scope.ActorID, CurrentSessionID: latest.SessionID, ReplyID: "latest", Text: "继续", Fetch: func(context.Context, string) (ReferencedMessage, bool) {
		calls++
		return ReferencedMessage{Segments: []platform.MessageSegment{{Type: platform.SegmentImage, PlatformFileID: "image"}}}, true
	}}
	got := Apply(ctx, opts)
	if calls != 0 || got.Text != "继续" || len(got.ReferenceSegments) != 0 || len(got.Reply.Segments) != 0 || got.Reply.Text != "" {
		t.Fatalf("latest injected reference = %#v", got)
	}
	opts.ReplyID = "old"
	got = Apply(ctx, opts)
	if calls != 1 || got.ForkFromMessageID != first.ID || len(got.ReferenceSegments) != 1 || got.Text != "继续" {
		t.Fatalf("older media fork = %#v", got)
	}
	opts.CurrentSessionID = "another-session"
	opts.ReplyID = "latest"
	got = Apply(ctx, opts)
	if calls != 2 || got.ForkFromMessageID != "" || len(got.ReferenceSegments) != 1 || got.Text == "继续" {
		t.Fatalf("same owner other session = %#v", got)
	}
}
