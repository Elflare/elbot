package media

import (
	"context"
	"path/filepath"
	"testing"

	"elbot/internal/delivery"
	"elbot/internal/platform"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

type historyResolver struct{ calls int }

func (r *historyResolver) ResolveMedia(_ context.Context, s platform.MessageSegment, _ int64) (delivery.Source, error) {
	r.calls++
	return delivery.Source{Data: []byte(s.PlatformFileID), MIMEType: "image/png"}, nil
}

func TestGetHistoryMediaReusesAssociation(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "main.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	m := NewManager(store, root, &LocalBackend{Root: root})
	row := storage.ChatMessage{ID: "h", Platform: "p", PlatformScopeID: "s", PlatformMessageID: "1", Segments: platform.MarshalChatSegments([]platform.MessageSegment{{Type: platform.SegmentText, Text: "hi"}, {Type: platform.SegmentImage, PlatformFileID: "one"}, {Type: platform.SegmentFile, PlatformFileID: "two"}})}
	resolver := &historyResolver{}
	ids, err := m.HistoryIDs(ctx, row)
	if err != nil || len(ids) != 0 || resolver.calls != 0 {
		t.Fatalf("ids = %v %v", ids, err)
	}
	first, err := m.GetHistoryMedia(ctx, row, 1, resolver)
	if err != nil {
		t.Fatal(err)
	}
	again, err := m.GetHistoryMedia(ctx, row, 1, resolver)
	if err != nil || first.ID != again.ID || resolver.calls != 1 {
		t.Fatalf("repeat = %#v %v calls %d", again, err, resolver.calls)
	}
	second, err := m.GetHistoryMedia(ctx, row, 2, resolver)
	if err != nil || second.ID == first.ID {
		t.Fatalf("second = %#v %v", second, err)
	}
	for _, index := range []int{0, 3} {
		if _, err := m.GetHistoryMedia(ctx, row, index, resolver); err == nil {
			t.Fatalf("accepted %d", index)
		}
	}
	ids, err = m.HistoryIDs(ctx, row)
	if err != nil || len(ids) != 2 || ids[1] != first.ID || ids[2] != second.ID {
		t.Fatalf("ids = %#v %v", ids, err)
	}
	row.PlatformScopeID = "other"
	ids, err = m.HistoryIDs(ctx, row)
	if err != nil || len(ids) != 0 {
		t.Fatal("scope leaked", ids, err)
	}
}
