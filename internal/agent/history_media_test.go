package agent

import (
	"context"
	"path/filepath"
	"testing"

	"elbot/internal/media"
	"elbot/internal/platform"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

func TestInboundMediaAssociatesHistoryPositions(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	history, err := sqlite.NewChatHistory(ctx, filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer history.Close()
	root := t.TempDir()
	center := media.NewManager(store, root, &media.LocalBackend{Root: root})
	center.History = history.Repository()
	a := &Agent{media: center}
	resolver := &inboundMediaResolver{}
	segments := []platform.MessageSegment{{Type: platform.SegmentImage, PlatformFileID: "bad", Size: center.MaxImportBytes + 1}, {Type: platform.SegmentText, Text: "hello"}, {Type: platform.SegmentImage, PlatformFileID: "good"}}
	row := &storage.ChatMessage{Platform: "p", PlatformScopeID: "s", PlatformMessageID: "1", SenderID: "u", Segments: platform.MarshalChatSegments(segments)}
	if err := center.History.Append(ctx, row); err != nil {
		t.Fatal(err)
	}
	ctx = platform.WithMessageContext(ctx, platform.MessageContext{Platform: "p", ScopeID: "s", PlatformMessageID: "1", Segments: segments, MediaResolver: resolver})
	resolved, _ := platform.MessageContextFrom(a.materializePlatformMedia(ctx))
	ids, err := center.HistoryIDs(ctx, *row)
	if err != nil || len(ids) != 1 || ids[2] != resolved.Segments[2].MediaID {
		t.Fatalf("position association = %#v %v", ids, err)
	}
	item, err := center.GetHistoryMedia(ctx, *row, 2, resolver)
	if err != nil || item.ID != ids[2] || resolver.calls != 1 {
		t.Fatalf("reimport = %#v %v calls %d", item, err, resolver.calls)
	}
	original, err := center.History.GetByPlatformMessage(ctx, "p", "s", "1")
	if err != nil || original.Segments != row.Segments {
		t.Fatalf("raw history mutated: %#v %v", original, err)
	}
}
