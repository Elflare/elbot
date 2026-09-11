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

func TestLatestAssistantResumesWithoutMediaAndOlderForkRestores(t *testing.T) {
	ctx := context.Background()
	store := newRefTestStore(t)
	scope := session.Scope{ActorID: "telegram:1", Platform: "telegram", PlatformScopeID: "group:9"}
	first, latest := createAssistantMessages(t, ctx, store, scope)
	mapPlatformMessage(t, ctx, store, scope, "old", first)
	mapPlatformMessage(t, ctx, store, scope, "latest", latest)
	calls := 0
	opts := Options{Store: store, Platform: scope.Platform, ScopeID: scope.PlatformScopeID, ActorID: scope.ActorID, ReplyID: "latest", Text: "继续", Fetch: func(context.Context, string) (ReferencedMessage, bool) {
		calls++
		return ReferencedMessage{Segments: []platform.MessageSegment{{Type: platform.SegmentImage, PlatformFileID: "image"}}}, true
	}}
	got := Apply(ctx, opts)
	if calls != 0 || got.ResumeSessionID != latest.SessionID || got.Text != "继续" || len(got.ReferenceSegments) != 0 || len(got.Reply.Segments) != 0 || got.Reply.Text != "" {
		t.Fatalf("latest injected reference = %#v", got)
	}
	opts.ReplyID = "old"
	got = Apply(ctx, opts)
	if calls != 1 || got.ForkFromMessageID != first.ID || got.ResumeSessionID != "" || len(got.ReferenceSegments) != 1 || got.Text != "继续" {
		t.Fatalf("older media fork = %#v", got)
	}
	opts.ReplyID = "latest"
	got = Apply(ctx, opts)
	if calls != 1 || got.ResumeSessionID != latest.SessionID || got.ForkFromMessageID != "" || len(got.ReferenceSegments) != 0 || got.Text != "继续" {
		t.Fatalf("same owner other session = %#v", got)
	}
}

func TestReferenceHistoryRestoresStableMediaAssociations(t *testing.T) {
	ctx := context.Background()
	store := newRefTestStore(t)
	root := t.TempDir()
	center := media.NewManager(store, root, &media.LocalBackend{Root: root})
	image, err := center.ImportBytes(ctx, []byte("image body"), media.Input{Name: "image.png", MIMEType: "image/png"})
	if err != nil {
		t.Fatal(err)
	}
	file, err := center.ImportBytes(ctx, []byte("file body"), media.Input{Name: "file.txt", MIMEType: "text/plain"})
	if err != nil {
		t.Fatal(err)
	}
	history, err := sqlite.NewChatHistory(ctx, filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer history.Close()

	raw := []platform.MessageSegment{
		{Type: platform.SegmentText, Text: "caption"},
		{Type: platform.SegmentImage, URL: "https://cdn.example.com/image.png?rkey=expired-secret"},
		{Type: platform.SegmentFile, PlatformFileID: "file-id"},
		{Type: platform.SegmentImage, URL: "https://cdn.example.com/image.png?rkey=expired-secret"},
		{Type: platform.SegmentFile, PlatformFileID: "stale-file-id"},
	}
	row := &storage.ChatMessage{
		Platform:          "qqofficial",
		PlatformScopeID:   "group:9",
		PlatformMessageID: "history-associated",
		SenderID:          "other",
		Text:              "caption",
		Segments:          platform.MarshalChatSegments(raw),
	}
	if err := history.Repository().Append(ctx, row); err != nil {
		t.Fatal(err)
	}
	for _, association := range []storage.HistoryMedia{
		{HistoryID: row.ID, Platform: row.Platform, ScopeID: row.PlatformScopeID, MessageID: row.PlatformMessageID, MediaIndex: 1, Kind: "image", MediaID: image.ID},
		{HistoryID: row.ID, Platform: row.Platform, ScopeID: row.PlatformScopeID, MessageID: row.PlatformMessageID, MediaIndex: 2, Kind: "file", MediaID: file.ID},
		{HistoryID: row.ID, Platform: row.Platform, ScopeID: row.PlatformScopeID, MessageID: row.PlatformMessageID, MediaIndex: 3, Kind: "image", MediaID: image.ID},
		{HistoryID: "replaced-history-row", Platform: row.Platform, ScopeID: row.PlatformScopeID, MessageID: row.PlatformMessageID, MediaIndex: 4, Kind: "file", MediaID: file.ID},
	} {
		if err := store.Media().SaveHistory(ctx, association); err != nil {
			t.Fatal(err)
		}
	}
	// An association with the same message ID in another scope must not leak in.
	if err := store.Media().SaveHistory(ctx, storage.HistoryMedia{
		HistoryID: "other-row", Platform: row.Platform, ScopeID: "group:other",
		MessageID: row.PlatformMessageID, MediaIndex: 1, Kind: "image", MediaID: file.ID,
	}); err != nil {
		t.Fatal(err)
	}

	fetchCalls := 0
	got := Apply(ctx, Options{
		Store: store, ChatHistory: history.Repository(),
		Platform: row.Platform, ScopeID: row.PlatformScopeID, ReplyID: row.PlatformMessageID,
		Text: "看看", Fetch: func(context.Context, string) (ReferencedMessage, bool) {
			fetchCalls++
			return ReferencedMessage{}, false
		},
	})
	if fetchCalls != 0 {
		t.Fatalf("platform fetch called %d times", fetchCalls)
	}
	if len(got.ReferenceSegments) != len(raw) {
		t.Fatalf("segments = %#v", got.ReferenceSegments)
	}
	if got.ReferenceSegments[0].Type != platform.SegmentText ||
		got.ReferenceSegments[1].MediaID != image.ID ||
		got.ReferenceSegments[2].MediaID != file.ID ||
		got.ReferenceSegments[3].MediaID != image.ID {
		t.Fatalf("restored segments = %#v", got.ReferenceSegments)
	}
	if got.ReferenceSegments[4].MediaID != "" || got.ReferenceSegments[4].PlatformFileID != "stale-file-id" {
		t.Fatalf("stale association reused = %#v", got.ReferenceSegments[4])
	}
	if got.ReferenceSegments[1].URL != "" || got.ReferenceSegments[3].URL != "" {
		t.Fatalf("expired URL survived history cleaning: %#v", got.ReferenceSegments)
	}
}
