package media

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/llm"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

type requestCountingStore struct {
	storage.Store
	media *requestCountingMedia
	refs  *requestCountingReferences
}

func (s *requestCountingStore) Media() storage.MediaRepository                    { return s.media }
func (s *requestCountingStore) MediaReferences() storage.MediaReferenceRepository { return s.refs }

type requestCountingMedia struct {
	storage.MediaRepository
	gets map[string]int
}

func (r *requestCountingMedia) Get(ctx context.Context, id string) (*storage.Media, error) {
	r.gets[id]++
	return r.MediaRepository.Get(ctx, id)
}

type requestCountingReferences struct {
	storage.MediaReferenceRepository
	adds map[string]int
}

func (r *requestCountingReferences) Add(ctx context.Context, ref *storage.MediaReference) error {
	r.adds[ref.MediaID]++
	return r.MediaReferenceRepository.Add(ctx, ref)
}

func TestAcquireForLLMReadsAndHoldsEachMediaOnce(t *testing.T) {
	for _, mode := range []string{"base64", "existing S3", "first upload"} {
		t.Run(mode, func(t *testing.T) {
			m := newConcurrentTestManager(t)
			m.Remote = &concurrentTestBackend{Backend: &LocalBackend{Root: t.TempDir()}}
			var segments []llm.MessageSegment
			for _, body := range []string{"first", "second"} {
				item, err := m.ImportBytes(t.Context(), []byte(body), Input{})
				if err != nil {
					t.Fatal(err)
				}
				if mode == "existing S3" {
					item.ObjectKey = "media/" + item.ID
					if err := m.Store.Media().Upsert(t.Context(), item); err != nil {
						t.Fatal(err)
					}
				}
				segment := llm.MessageSegment{Type: llm.SegmentFile, MediaID: item.ID}
				segments = append(segments, segment, segment)
			}
			if mode != "base64" {
				m.FileDelivery.Backend = "s3"
			}
			counter := &requestCountingStore{
				Store: m.Store,
				media: &requestCountingMedia{MediaRepository: m.Store.Media(), gets: map[string]int{}},
				refs:  &requestCountingReferences{MediaReferenceRepository: m.Store.MediaReferences(), adds: map[string]int{}},
			}
			m.Store = counter
			out, release, err := m.AcquireForLLM(t.Context(), []llm.LLMMessage{{Role: llm.RoleUser, Segments: segments}})
			defer release()
			if err != nil {
				t.Fatal(err)
			}
			wantGets := 1
			if mode == "first upload" {
				wantGets = 2
			}
			for i, segment := range segments {
				if got := counter.media.gets[segment.MediaID]; got != wantGets {
					t.Errorf("%s metadata reads=%d, want %d", segment.MediaID, got, wantGets)
				}
				if got := counter.refs.adds[segment.MediaID]; got != 1 {
					t.Errorf("%s holds=%d, want 1", segment.MediaID, got)
				}
				if out[0].Segments[i].URL == "" {
					t.Fatal("missing resolved URL")
				}
			}
		})
	}
}

func TestAcquireForLLMReleasesHoldsOnSuccessAndFailure(t *testing.T) {
	for _, mode := range []string{"success", "missing media", "resolve failure"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "store.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			root := t.TempDir()
			manager := NewManager(store, root, &LocalBackend{Root: root})
			item, err := manager.ImportBytes(ctx, []byte("hello"), Input{Name: "input.txt", MIMEType: "text/plain"})
			if err != nil {
				t.Fatal(err)
			}
			segment := llm.MessageSegment{Type: llm.SegmentFile, MediaID: item.ID}
			messages := []llm.LLMMessage{{Role: llm.RoleUser, Segments: []llm.MessageSegment{segment, segment}}}
			if mode == "missing media" {
				messages[0].Segments = append(messages[0].Segments, llm.MessageSegment{Type: llm.SegmentFile, MediaID: IDPrefix + strings.Repeat("f", 64)})
			}
			if mode == "resolve failure" {
				manager.FileDelivery.MaxDirectBase64Bytes = 1
			}
			resolved, release, err := manager.AcquireForLLM(ctx, messages)
			if (mode == "resolve failure") != (err != nil) {
				t.Fatalf("mode=%s error=%v", mode, err)
			}
			if mode == "missing media" && (resolved[0].Segments[2].Type != llm.SegmentText || messages[0].Segments[2].MediaID == "") {
				t.Fatal("missing media must only change the request copy")
			}
			if mode != "resolve failure" {
				refs, err := store.MediaReferences().ListMediaIDs(ctx, item.ID)
				if err != nil || len(refs) != 1 || refs[0].OwnerType != "request" {
					t.Fatalf("duplicate holds or missing request ownership: %+v / %v", refs, err)
				}
				if resolved[0].Segments[0].URL == "" || messages[0].Segments[0].URL != "" {
					t.Fatal("request material did not stay in a request copy")
				}
			}
			cancel()
			release()
			refs, err := store.MediaReferences().ListMediaIDs(context.Background(), item.ID)
			if err != nil || len(refs) != 0 {
				t.Fatalf("holds survived release or error: %+v / %v", refs, err)
			}
		})
	}
}
