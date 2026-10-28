package media

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"elbot/internal/delivery"
	"elbot/internal/testutil"
)

type batchResolver struct{ calls int }

func (r *batchResolver) ResolveMedia(_ context.Context, _ string, segment PlatformMedia, _ int64) (delivery.Source, error) {
	r.calls++
	if segment.PlatformFileID == "fail" {
		return delivery.Source{}, fmt.Errorf("unavailable")
	}
	return delivery.Source{Data: []byte(segment.PlatformFileID), MIMEType: "text/plain"}, nil
}

func TestHistoryBatchOwnsBudgetCacheAndRepeatedFailures(t *testing.T) {
	ctx := t.Context()
	store := testutil.NewSQLiteStore(t)
	root := t.TempDir()
	center := NewManager(store, root, &LocalBackend{Root: root})
	var segments []PlatformMedia
	for _, id := range []string{"fail", "two", "cached", "four"} {
		segments = append(segments, PlatformMedia{Type: "file", PlatformFileID: id})
	}
	row := HistoryMessage{ID: "history", Platform: "p", ScopeID: "s", MessageID: "m", Media: segments}
	resolver := &batchResolver{}
	cached, err := center.GetHistoryMedia(ctx, row, 3, resolver.ResolveMedia)
	if err != nil {
		t.Fatal(err)
	}
	var requests []HistoryMediaRequest
	for _, index := range []int{1, 1, 2, 2, 4, 3, 0, 8} {
		requests = append(requests, HistoryMediaRequest{Message: row, Index: index})
	}
	results, err := center.GetHistoryMediaBatch(ctx, requests, resolver.ResolveMedia, HistoryFetchOptions{MaxFetchAttempts: 2})
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
	results, err = center.GetHistoryMediaBatch(ctx, []HistoryMediaRequest{{Message: row, Index: 4}}, resolver.ResolveMedia, HistoryFetchOptions{MaxFetchAttempts: 1})
	if err != nil || results[0].Err != nil || resolver.calls != 4 {
		t.Fatalf("new call inherited budget: %+v %v", results, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := center.GetHistoryMediaBatch(canceled, requests, resolver.ResolveMedia, HistoryFetchOptions{}); !errors.Is(err, context.Canceled) || resolver.calls != 4 {
		t.Fatalf("cancellation=%v calls=%d", err, resolver.calls)
	}
}

func TestHistoryBatchBudgetSpansMessages(t *testing.T) {
	ctx := t.Context()
	store := testutil.NewSQLiteStore(t)
	root := t.TempDir()
	center := NewManager(store, root, &LocalBackend{Root: root})
	resolver := &batchResolver{}
	var requests []HistoryMediaRequest
	for _, id := range []string{"one", "two"} {
		row := HistoryMessage{ID: id, Platform: "p", ScopeID: "s", MessageID: id, Media: []PlatformMedia{{Type: "file", PlatformFileID: id}}}
		requests = append(requests, HistoryMediaRequest{Message: row, Index: 1})
	}
	results, err := center.GetHistoryMediaBatch(ctx, requests, resolver.ResolveMedia, HistoryFetchOptions{MaxFetchAttempts: 1})
	if err != nil || results[0].Err != nil || !errors.Is(results[1].Err, ErrHistoryFetchLimit) || resolver.calls != 1 {
		t.Fatalf("results=%+v calls=%d err=%v", results, resolver.calls, err)
	}
}

func TestHistoryBatchPassesMessagePlatformAndReusesCache(t *testing.T) {
	store := testutil.NewSQLiteStore(t)
	root := t.TempDir()
	center := NewManager(store, root, &LocalBackend{Root: root})
	var requests []HistoryMediaRequest
	for _, name := range []string{"telegram", "qqonebot"} {
		requests = append(requests, HistoryMediaRequest{Message: HistoryMessage{ID: "history", Platform: name, ScopeID: "scope", MessageID: "message", Media: []PlatformMedia{{Type: "file", PlatformFileID: "file"}}}, Index: 1})
	}
	var seen []string
	resolve := func(_ context.Context, name string, input PlatformMedia, limit int64) (delivery.Source, error) {
		seen = append(seen, name)
		if input.PlatformFileID != "file" || limit != center.MaxImportBytes {
			t.Fatalf("input=%+v limit=%d", input, limit)
		}
		return delivery.Source{Data: []byte(name)}, nil
	}
	results, err := center.GetHistoryMediaBatch(t.Context(), requests, resolve, HistoryFetchOptions{MaxFetchAttempts: 2})
	if err != nil || len(results) != 2 || results[0].Err != nil || results[1].Err != nil || len(seen) != 2 || seen[0] != "telegram" || seen[1] != "qqonebot" {
		t.Fatalf("results=%+v seen=%v error=%v", results, seen, err)
	}
	again, err := center.GetHistoryMediaBatch(t.Context(), requests, resolve, HistoryFetchOptions{MaxFetchAttempts: 1})
	if err != nil || len(seen) != 2 || len(again) != 2 || again[0].Err != nil || again[1].Err != nil || again[0].Media.ID != results[0].Media.ID || again[1].Media.ID != results[1].Media.ID {
		t.Fatalf("cache=%+v seen=%v error=%v", again, seen, err)
	}
}
