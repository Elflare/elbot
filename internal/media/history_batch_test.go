package media

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"elbot/internal/delivery"
	"elbot/internal/platform"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

type batchResolver struct{ calls int }

func (r *batchResolver) ResolveMedia(_ context.Context, segment platform.MessageSegment, _ int64) (delivery.Source, error) {
	r.calls++
	if segment.PlatformFileID == "fail" {
		return delivery.Source{}, fmt.Errorf("unavailable")
	}
	return delivery.Source{Data: []byte(segment.PlatformFileID), MIMEType: "text/plain"}, nil
}

func TestHistoryBatchOwnsBudgetCacheAndRepeatedFailures(t *testing.T) {
	ctx := t.Context()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "main.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	center := NewManager(store, root, &LocalBackend{Root: root})
	var segments []platform.MessageSegment
	for _, id := range []string{"fail", "two", "cached", "four"} {
		segments = append(segments, platform.MessageSegment{Type: platform.SegmentFile, PlatformFileID: id})
	}
	row := storage.ChatMessage{ID: "history", Platform: "p", PlatformScopeID: "s", PlatformMessageID: "m", Segments: platform.MarshalChatSegments(segments)}
	resolver := &batchResolver{}
	cached, err := center.GetHistoryMedia(ctx, row, 3, resolver)
	if err != nil {
		t.Fatal(err)
	}
	var requests []HistoryMediaRequest
	for _, index := range []int{1, 1, 2, 2, 4, 3, 0, 8} {
		requests = append(requests, HistoryMediaRequest{Message: row, Index: index})
	}
	results, err := center.GetHistoryMediaBatch(ctx, requests, resolver, HistoryFetchOptions{MaxFetchAttempts: 2})
	if err != nil || len(results) != len(requests) || resolver.calls != 3 {
		t.Fatalf("results=%+v calls=%d err=%v", results, resolver.calls, err)
	}
	if results[0].Err == nil || results[1].Err != results[0].Err || results[2].Err != nil || results[2].Media.ID != results[3].Media.ID {
		t.Fatalf("dedup=%+v", results)
	}
	if !errors.Is(results[4].Err, ErrHistoryFetchLimit) || results[5].Err != nil || results[5].Media.ID != cached.ID {
		t.Fatalf("cache after limit=%+v", results)
	}
	for _, index := range []int{6, 7} {
		if !errors.Is(results[index].Err, ErrHistoryMediaIndex) {
			t.Fatalf("index result=%+v", results[index])
		}
	}
	results, err = center.GetHistoryMediaBatch(ctx, []HistoryMediaRequest{{Message: row, Index: 4}}, resolver, HistoryFetchOptions{MaxFetchAttempts: 1})
	if err != nil || results[0].Err != nil || resolver.calls != 4 {
		t.Fatalf("new call inherited budget: %+v %v", results, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := center.GetHistoryMediaBatch(canceled, requests, resolver, HistoryFetchOptions{}); !errors.Is(err, context.Canceled) || resolver.calls != 4 {
		t.Fatalf("cancellation=%v calls=%d", err, resolver.calls)
	}
}

func TestHistoryBatchBudgetSpansMessages(t *testing.T) {
	ctx := t.Context()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "main.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	center := NewManager(store, root, &LocalBackend{Root: root})
	resolver := &batchResolver{}
	var requests []HistoryMediaRequest
	for _, id := range []string{"one", "two"} {
		row := storage.ChatMessage{ID: id, Platform: "p", PlatformScopeID: "s", PlatformMessageID: id, Segments: platform.MarshalChatSegments([]platform.MessageSegment{{Type: platform.SegmentFile, PlatformFileID: id}})}
		requests = append(requests, HistoryMediaRequest{Message: row, Index: 1})
	}
	results, err := center.GetHistoryMediaBatch(ctx, requests, resolver, HistoryFetchOptions{MaxFetchAttempts: 1})
	if err != nil || results[0].Err != nil || !errors.Is(results[1].Err, ErrHistoryFetchLimit) || resolver.calls != 1 {
		t.Fatalf("results=%+v calls=%d err=%v", results, resolver.calls, err)
	}
}
