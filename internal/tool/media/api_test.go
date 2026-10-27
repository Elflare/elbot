package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/media"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
	"elbot/internal/tool/share"
)

func newMediaAPITest(t *testing.T) (*MediaRuntime, *media.Manager) {
	t.Helper()
	store, err := sqlite.New(t.Context(), filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	root := t.TempDir()
	center := media.NewManager(store, root, &media.LocalBackend{Root: root})
	return NewMediaRuntime(center, t.TempDir()), center
}

func TestMediaAPIMetadataAndImageValidation(t *testing.T) {
	runtime, center := newMediaAPITest(t)
	var imageData bytes.Buffer
	if err := png.Encode(&imageData, image.NewNRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	record, err := center.ImportBytes(t.Context(), imageData.Bytes(), media.Input{Name: "image.png", MIMEType: "image/png"})
	if err != nil {
		t.Fatal(err)
	}
	want := share.MediaInfo{ID: record.ID, Name: "image.png", MIMEType: "image/png", Size: int64(imageData.Len())}
	for _, get := range []func(context.Context, string) (share.MediaInfo, error){runtime.Metadata, runtime.ValidateImage} {
		got, err := get(t.Context(), record.ID)
		if err != nil || got != want {
			t.Fatalf("info=%+v want=%+v err=%v", got, want, err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := get(ctx, record.ID); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation=%v", err)
		}
	}
	for _, data := range [][]byte{[]byte("not an image"), imageData.Bytes()[:33]} {
		invalid, err := center.ImportBytes(t.Context(), data, media.Input{Name: "broken.png", MIMEType: "image/png"})
		if err != nil {
			t.Fatal(err)
		}
		got, err := runtime.ValidateImage(t.Context(), invalid.ID)
		if err == nil || got != (share.MediaInfo{}) {
			t.Fatalf("accepted invalid image: %+v %v", got, err)
		}
	}
	call := runtime.NewCall()
	defer call.Close()
	info, err := call.Retain(t.Context(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	info.Name = "changed"
	info.ID = "changed"
	again, err := call.Retain(t.Context(), record.ID)
	if err != nil || again != want {
		t.Fatalf("caller changed retained metadata: %+v %v", again, err)
	}
}

func TestMediaAPIImportsAndNormalizedCallPath(t *testing.T) {
	runtime, _ := newMediaAPITest(t)
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(base, "nested", "file.txt")
	if err := os.WriteFile(path, []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := runtime.ImportFile(t.Context(), path, share.MediaImportOptions{Name: "file.txt", MIMEType: "text/plain"})
	if err != nil || info.Name != "file.txt" || info.Size != 5 {
		t.Fatalf("file=%+v %v", info, err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("hello"))
	}))
	defer server.Close()
	remote, err := runtime.ImportURL(t.Context(), server.URL+"/file.txt", share.MediaImportOptions{})
	if err != nil || remote.ID != info.ID || remote.Size != 5 {
		t.Fatalf("url=%+v %v", remote, err)
	}
	call := runtime.NewCall()
	defer call.Close()
	imported, err := call.Import(t.Context(), base, "nested\\file.txt", share.MediaImportOptions{})
	if err != nil || imported.ID != info.ID {
		t.Fatalf("normalized import=%+v %v", imported, err)
	}
	for _, path := range []string{"nested/../nested/file.txt", "nested\\..\\nested\\file.txt", "~/file.txt", "file:stream"} {
		if _, err := call.Import(t.Context(), base, path, share.MediaImportOptions{}); err == nil {
			t.Fatalf("accepted %q", path)
		}
	}
}

func TestMediaExportsRejectInvalidDecodedIDs(t *testing.T) {
	runtime, _ := newMediaAPITest(t)
	for _, raw := range []string{`null`, `{}`, `{"media":null}`, `{"media":""}`, `{"media":"bad"}`} {
		t.Run(raw, func(t *testing.T) {
			var input share.MediaInput
			if err := json.Unmarshal([]byte(raw), &input); err != nil {
				t.Fatal(err)
			}
			call := runtime.NewCall()
			defer call.Close()
			if _, _, err := call.Export(t.Context(), input.MediaID, t.TempDir()); err == nil || !strings.Contains(err.Error(), "invalid media ID") {
				t.Fatalf("export accepted invalid ID: %v", err)
			}
			if _, err := call.CachedExport(t.Context(), input.MediaID); err == nil || !strings.Contains(err.Error(), "invalid media ID") {
				t.Fatalf("cached export accepted invalid ID: %v", err)
			}
		})
	}
}

func TestMediaAPIHistoryBatchPreservesOrderAndErrors(t *testing.T) {
	runtime, center := newMediaAPITest(t)
	history, err := sqlite.NewChatHistory(t.Context(), filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer history.Close()
	center.History = history.Repository()
	record, err := center.ImportBytes(t.Context(), []byte("file"), media.Input{Name: "file.txt", MIMEType: "text/plain"})
	if err != nil {
		t.Fatal(err)
	}
	stored := storage.ChatMessage{Platform: "test", PlatformScopeID: "chat", PlatformMessageID: "message", Segments: `[{"type":"file","platform_file_id":"file"}]`}
	if err := center.History.Append(t.Context(), &stored); err != nil {
		t.Fatal(err)
	}
	row := media.HistoryMessage{ID: stored.ID, Platform: "test", ScopeID: "chat", MessageID: "message", Media: []media.PlatformMedia{{Type: "file", PlatformFileID: "file"}}}
	if err := center.AssociateHistory(t.Context(), row, 1, "file", record.ID); err != nil {
		t.Fatal(err)
	}
	requests := []media.HistoryMediaRequest{{Message: row, Index: 1}, {Message: row, Index: 2}, {Message: row, Index: 1}}
	results, err := runtime.GetHistoryMediaBatch(t.Context(), requests, nil, media.HistoryFetchOptions{MaxFetchAttempts: 1})
	if err != nil || len(results) != 3 {
		t.Fatalf("results=%+v %v", results, err)
	}
	want := share.MediaInfo{ID: record.ID, Name: record.Name, MIMEType: record.MIMEType, Size: record.Size}
	if results[0].Err != nil || results[0].Media != want || results[2].Err != nil || results[2].Media != want {
		t.Fatalf("order or cache lost: %+v", results)
	}
	if !errors.Is(results[1].Err, media.ErrHistoryMediaIndex) || results[1].Media != (share.MediaInfo{}) {
		t.Fatalf("item error=%+v", results[1])
	}
	ids, err := runtime.HistoryIDs(t.Context(), row)
	if err != nil || ids[1] != record.ID {
		t.Fatalf("ids=%v err=%v", ids, err)
	}
}
