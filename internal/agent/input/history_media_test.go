package input

import (
	"context"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/media"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
	"elbot/internal/testutil/mediatest"
	"path/filepath"
	"testing"
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
	center := mediatest.New(t, store, root)
	center.History = history.Repository()
	a := &MessageHandler{media: center, History: center.History}
	resolver := &inboundMediaResolver{}
	segments := []agentshare.MessageSegment{{Type: "image", Media: media.PlatformMedia{Type: "image", PlatformFileID: "bad", Size: config.Default().PlatformFiles.MaxReceiveFileBytes + 1}}, {Type: "text", Text: "hello"}, {Type: "image", Media: media.PlatformMedia{Type: "image", PlatformFileID: "good"}}}
	row := &storage.ChatMessage{Platform: "p", PlatformScopeID: "s", PlatformMessageID: "1", SenderID: "u", Segments: storage.MarshalChatSegments([]storage.ChatSegment{
		{Type: "image", PlatformFileID: "bad", Size: config.Default().PlatformFiles.MaxReceiveFileBytes + 1}, {Type: "text", Text: "hello"}, {Type: "image", PlatformFileID: "good"},
	})}
	if err := history.Repository().Append(ctx, row); err != nil {
		t.Fatal(err)
	}
	ctx = agentshare.WithMessageContext(contextinfo.WithConversation(ctx, contextinfo.Conversation{Source: contextinfo.Source{Platform: "p", ScopeID: "s"}, PlatformMessageID: "1"}), agentshare.MessageContext{

		Segments:     segments,
		ResolveMedia: resolver.ResolveMedia,
	})
	prepared, err := a.materializePlatformMedia(ctx)
	if err != nil {
		t.Fatal(err)
	}
	resolved, _ := agentshare.MessageContextFrom(prepared)
	historyMessage := media.HistoryMessage{ID: row.ID, Platform: "p", ScopeID: "s", MessageID: "1", Media: []media.PlatformMedia{segments[0].Media, segments[2].Media}}
	ids, err := center.HistoryIDs(ctx, historyMessage)
	if err != nil || len(ids) != 1 || ids[2] != resolved.Segments[2].Media.MediaID {
		t.Fatalf("position association = %#v %v", ids, err)
	}
	items, err := center.GetHistoryMediaBatch(ctx, []media.HistoryMediaRequest{{Message: historyMessage, Index: 2}}, func(ctx context.Context, _ string, source media.PlatformMedia, limit int64) (delivery.Source, error) {
		return resolver.ResolveMedia(ctx, source, limit)
	}, media.HistoryFetchOptions{MaxFetchAttempts: 1})
	if err != nil || len(items) != 1 || items[0].Err != nil || items[0].Media.ID != ids[2] || resolver.calls != 1 {
		t.Fatalf("reimport = %#v %v calls %d", items, err, resolver.calls)
	}
	original, err := center.History.GetByPlatformMessage(ctx, "p", "s", "1")
	if err != nil || original.Segments != row.Segments {
		t.Fatalf("raw history mutated: %#v %v", original, err)
	}
}
