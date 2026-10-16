package media

import (
	"testing"
	"time"

	"elbot/internal/platform"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

func TestHistoryCacheMaintainsMatchingAssociations(t *testing.T) {
	for _, name := range []string{"history", "output", "same output", "changed output", "old history", "changed kind"} {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			base, err := sqlite.New(ctx, ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = base.Close() })
			root := t.TempDir()
			center := NewManager(base, root, &LocalBackend{Root: root})
			item, err := center.ImportBytes(ctx, []byte("cached"), Input{Name: "file"})
			if err != nil {
				t.Fatal(err)
			}
			row := storage.ChatMessage{ID: "history", Platform: "p", PlatformScopeID: "s", PlatformMessageID: "m", Segments: platform.MarshalChatSegments([]platform.MessageSegment{{Type: platform.SegmentFile, PlatformFileID: "cached"}})}
			if name != "output" {
				association := storage.HistoryMedia{HistoryID: row.ID, Platform: "p", ScopeID: "s", MessageID: "m", MediaIndex: 1, Kind: "file", MediaID: item.ID}
				if name == "old history" {
					association.HistoryID = "deleted history"
				}
				if name == "changed kind" {
					association.Kind = "image"
				}
				if err := base.Media().SaveHistory(ctx, association); err != nil {
					t.Fatal(err)
				}
			}
			if name == "changed output" {
				item, err = center.ImportBytes(ctx, []byte("replacement"), Input{Name: "replacement"})
				if err != nil {
					t.Fatal(err)
				}
			}
			if name == "output" || name == "same output" || name == "changed output" {
				if err := base.Media().SaveOutput(ctx, storage.MediaOutput{Platform: "p", ScopeID: "s", MessageID: "m", Kind: "file", MediaID: item.ID, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
					t.Fatal(err)
				}
			}
			before, err := base.Media().FindHistory(ctx, "p", "s", "m")
			if err != nil {
				t.Fatal(err)
			}
			resolver := &batchResolver{}
			results, err := center.GetHistoryMediaBatch(ctx, []HistoryMediaRequest{{Message: row, Index: 1}, {Message: row, Index: 1}}, resolver, HistoryFetchOptions{MaxFetchAttempts: 1})
			if err != nil || len(results) != 2 || results[0].Err != nil || results[1].Err != nil || results[0].Media.ID != item.ID || results[1].Media.ID != item.ID {
				t.Fatalf("results=%+v error=%v", results, err)
			}
			after, err := base.Media().FindHistory(ctx, "p", "s", "m")
			if err != nil || len(after) != 1 || after[0].HistoryID != row.ID || after[0].Kind != "file" || after[0].MediaID != item.ID {
				t.Fatalf("association=%+v error=%v", after, err)
			}
			if (name == "history" || name == "same output") && after[0].OwnerID != before[0].OwnerID {
				t.Error("cache hit replaced the persistent reference owner")
			}
		})
	}
}
