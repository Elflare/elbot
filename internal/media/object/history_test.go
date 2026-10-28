package object

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"elbot/internal/media/share"
	"elbot/internal/storage"
)

type testHistory func(context.Context, string, string, string) (*storage.ChatMessage, error)

func (h testHistory) GetByPlatformMessage(ctx context.Context, p, s, id string) (*storage.ChatMessage, error) {
	return h(ctx, p, s, id)
}

func TestHistoryMergesMatchingAssociationsAndOutputs(t *testing.T) {
	m, store, _ := newTestManager(t)
	first := importTestMedia(t, m, "first")
	second := importTestMedia(t, m, "second")
	key := share.HistoryKey{ID: "history", Platform: "test", ScopeID: "scope", MessageID: "message"}
	for _, index := range []int{1, 2, 3} {
		if err := m.AssociateHistory(t.Context(), key, index, "image", first.ID); err != nil {
			t.Fatal(err)
		}
	}
	for _, output := range []storage.MediaOutput{
		{Platform: "test", ScopeID: "scope", MessageID: "message", SegmentIndex: 0, Kind: "image", MediaID: first.ID, ExpiresAt: time.Now().Add(time.Hour)},
		{Platform: "test", ScopeID: "scope", MessageID: "message", SegmentIndex: 1, Kind: "file", MediaID: second.ID, ExpiresAt: time.Now().Add(time.Hour)},
		{Platform: "test", ScopeID: "scope", MessageID: "message", SegmentIndex: 2, Kind: "file", MediaID: second.ID, ExpiresAt: time.Now().Add(-time.Hour)},
	} {
		if err := store.Media().SaveOutput(t.Context(), output); err != nil {
			t.Fatal(err)
		}
	}
	gets := map[string]int{}
	m.media = testRepository{MediaRepository: m.media, get: func(ctx context.Context, id string) (*storage.Media, error) {
		gets[id]++
		return store.Media().Get(ctx, id)
	}}
	entries, err := m.LoadHistoryMedia(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || !entries[1].Associated || entries[1].Kind != "image" || entries[2].Associated || entries[2].Kind != "" || entries[2].Media.ID != second.ID || entries[3].Media.ID != first.ID {
		t.Fatalf("entries = %#v", entries)
	}
	if gets[first.ID] != 1 || gets[second.ID] != 1 {
		t.Fatalf("duplicate metadata queries = %v", gets)
	}
	key.ID = "replaced-history"
	ids, err := m.HistoryIDs(t.Context(), key)
	if err != nil || len(ids) != 2 || ids[1] != first.ID || ids[2] != second.ID {
		t.Fatalf("history identity mismatch = %v %v", ids, err)
	}
}

func TestReconcileHistoryPaginatesAndStopsOnQueryFailure(t *testing.T) {
	m, store, _ := newTestManager(t)
	item := importTestMedia(t, m, "history")
	key := share.HistoryKey{ID: "old-history", Platform: "test", ScopeID: "scope", MessageID: "message"}
	for index := 1; index <= 105; index++ {
		if err := m.AssociateHistory(t.Context(), key, index, "file", item.ID); err != nil {
			t.Fatal(err)
		}
	}
	failure := errors.New("history unavailable")
	unavailable := testHistory(func(context.Context, string, string, string) (*storage.ChatMessage, error) { return nil, failure })
	m.options.Now = func() time.Time { return time.Now().Add(2 * OrphanGrace) }
	if _, err := m.Cleanup(t.Context(), unavailable); !errors.Is(err, failure) {
		t.Fatalf("query failure = %v", err)
	}
	records, err := store.Media().FindHistory(t.Context(), "test", "scope", "message")
	if err != nil || len(records) != 105 {
		t.Fatalf("failure removed associations: %d %v", len(records), err)
	}
	record, _ := store.Media().Get(t.Context(), item.ID)
	if record.Deleting {
		t.Fatal("query failure did not stop cleanup")
	}
	calls := 0
	replaced := testHistory(func(context.Context, string, string, string) (*storage.ChatMessage, error) {
		calls++
		return &storage.ChatMessage{ID: "new-history"}, nil
	})
	if err := m.ReconcileHistory(t.Context(), replaced); err != nil {
		t.Fatal(err)
	}
	records, err = store.Media().FindHistory(t.Context(), "test", "scope", "message")
	if err != nil || len(records) != 0 || calls != 1 {
		t.Fatalf("pagination = %d queries=%d %v", len(records), calls, err)
	}
	if refs, _ := store.MediaReferences().ListMediaIDs(t.Context(), item.ID); len(refs) != 0 {
		t.Fatal("history references retained")
	}
}

func TestReconcileKeepsExistingAndDeletesConfirmedMissing(t *testing.T) {
	m, store, _ := newTestManager(t)
	item := importTestMedia(t, m, "history owners")
	for i := range 2 {
		key := share.HistoryKey{ID: fmt.Sprint(i), Platform: "test", ScopeID: "scope", MessageID: fmt.Sprint(i)}
		if err := m.AssociateHistory(t.Context(), key, 1, "file", item.ID); err != nil {
			t.Fatal(err)
		}
	}
	history := testHistory(func(_ context.Context, _, _, id string) (*storage.ChatMessage, error) {
		if id == "0" {
			return &storage.ChatMessage{ID: "0"}, nil
		}
		return nil, storage.ErrNotFound
	})
	if err := m.ReconcileHistory(t.Context(), history); err != nil {
		t.Fatal(err)
	}
	records, err := store.Media().ListHistory(t.Context(), "", 10)
	if err != nil || len(records) != 1 || records[0].HistoryID != "0" {
		t.Fatalf("associations = %#v %v", records, err)
	}
}
