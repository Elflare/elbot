package media

import (
	"context"
	"testing"

	"elbot/internal/delivery"
	"elbot/internal/testutil"
)

type historyResolver struct{ calls int }

func (r *historyResolver) ResolveMedia(_ context.Context, _ string, s PlatformMedia, _ int64) (delivery.Source, error) {
	r.calls++
	return delivery.Source{Data: []byte(s.PlatformFileID), MIMEType: "image/png"}, nil
}

func TestGetHistoryMediaReusesAssociation(t *testing.T) {
	ctx := context.Background()
	store := testutil.NewSQLiteStore(t)
	root := t.TempDir()
	m := NewManager(store, root, &LocalBackend{Root: root})
	row := HistoryMessage{ID: "h", Platform: "p", ScopeID: "s", MessageID: "1", Media: []PlatformMedia{{Type: "image", PlatformFileID: "one"}, {Type: "file", PlatformFileID: "two"}}}
	resolver := &historyResolver{}
	ids, err := m.HistoryIDs(ctx, row)
	if err != nil || len(ids) != 0 || resolver.calls != 0 {
		t.Fatalf("ids = %v %v", ids, err)
	}
	first, err := m.GetHistoryMedia(ctx, row, 1, resolver.ResolveMedia)
	if err != nil {
		t.Fatal(err)
	}
	again, err := m.GetHistoryMedia(ctx, row, 1, resolver.ResolveMedia)
	if err != nil || first.ID != again.ID || resolver.calls != 1 {
		t.Fatalf("repeat = %#v %v calls %d", again, err, resolver.calls)
	}
	second, err := m.GetHistoryMedia(ctx, row, 2, resolver.ResolveMedia)
	if err != nil || second.ID == first.ID {
		t.Fatalf("second = %#v %v", second, err)
	}
	for _, index := range []int{0, 3} {
		if _, err := m.GetHistoryMedia(ctx, row, index, resolver.ResolveMedia); err == nil {
			t.Fatalf("accepted %d", index)
		}
	}
	ids, err = m.HistoryIDs(ctx, row)
	if err != nil || len(ids) != 2 || ids[1] != first.ID || ids[2] != second.ID {
		t.Fatalf("ids = %#v %v", ids, err)
	}
	row.ScopeID = "other"
	ids, err = m.HistoryIDs(ctx, row)
	if err != nil || len(ids) != 0 {
		t.Fatal("scope leaked", ids, err)
	}
}
