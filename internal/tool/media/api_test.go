package media

import (
	"bytes"
	"context"
	"elbot/internal/operationresult"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"elbot/internal/media"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
	"elbot/internal/testutil/mediatest"
	"elbot/internal/tool/share"
)

func newMediaAPITest(t *testing.T, history media.History) (*MediaRuntime, *media.MediaCenter) {
	t.Helper()
	store, err := sqlite.New(t.Context(), filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	root := t.TempDir()
	center := mediatest.New(t, media.MediaCenterDeps{Store: store, ChatHistory: history}, root)
	return NewMediaRuntime(center, t.TempDir(), center.PublishUsageFinished), center
}

func TestMediaAPIMetadataAndImageValidation(t *testing.T) {
	runtime, center := newMediaAPITest(t, nil)
	var imageData bytes.Buffer
	if err := png.Encode(&imageData, image.NewNRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	record, err := center.ImportBytes(t.Context(), imageData.Bytes(), media.Input{Name: "image.png", MIMEType: "image/png"}, media.ImportLimits{})
	if err != nil {
		t.Fatal(err)
	}
	want := share.MediaInfo{ID: record.Data.ID, Name: "image.png", MIMEType: "image/png", Size: int64(imageData.Len())}
	for _, get := range []func(context.Context, string) (operationresult.Result[share.MediaInfo], error){runtime.Metadata, runtime.ValidateImage} {
		got, err := get(t.Context(), record.Data.ID)
		if err != nil || got.Status != operationresult.Success || got.Data != want {
			t.Fatalf("info=%+v want=%+v err=%v", got, want, err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := get(ctx, record.Data.ID); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation=%v", err)
		}
	}
	for _, data := range [][]byte{[]byte("not an image"), imageData.Bytes()[:33]} {
		invalid, err := center.ImportBytes(t.Context(), data, media.Input{Name: "broken.png", MIMEType: "image/png"}, media.ImportLimits{})
		if err != nil {
			t.Fatal(err)
		}
		got, err := runtime.ValidateImage(t.Context(), invalid.Data.ID)
		if err != nil || got.Status == operationresult.Success {
			t.Fatalf("accepted invalid image: %+v %v", got, err)
		}
	}
	call := runtime.NewCall()
	defer call.Close()
	info, err := call.Retain(t.Context(), record.Data.ID)
	if err != nil {
		t.Fatal(err)
	}
	info.Data.Name = "changed"
	info.Data.ID = "changed"
	again, err := call.Retain(t.Context(), record.Data.ID)
	if err != nil || again.Status != operationresult.Success || again.Data != want {
		t.Fatalf("caller changed retained metadata: %+v %v", again, err)
	}
}

func TestMediaAPIImportsAndNormalizedCallPath(t *testing.T) {
	runtime, _ := newMediaAPITest(t, nil)
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(base, "nested", "file.txt")
	if err := os.WriteFile(path, []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := runtime.ImportFile(t.Context(), path, share.MediaImportOptions{Name: "file.txt", MIMEType: "text/plain"})
	if err != nil || info.Data.Name != "file.txt" || info.Data.Size != 5 {
		t.Fatalf("file=%+v %v", info, err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("hello"))
	}))
	defer server.Close()
	remote, err := runtime.ImportURL(t.Context(), server.URL+"/file.txt", share.MediaImportOptions{})
	if err != nil || remote.Data.ID != info.Data.ID || remote.Data.Size != 5 {
		t.Fatalf("url=%+v %v", remote, err)
	}
	call := runtime.NewCall()
	defer call.Close()
	imported, err := call.Import(t.Context(), base, "nested\\file.txt", share.MediaImportOptions{})
	if err != nil || imported.Data.ID != info.Data.ID {
		t.Fatalf("normalized import=%+v %v", imported, err)
	}
	for _, path := range []string{"nested/../nested/file.txt", "nested\\..\\nested\\file.txt", "~/file.txt", "file:stream"} {
		if result, err := call.Import(t.Context(), base, path, share.MediaImportOptions{}); err != nil || result.Status == operationresult.Success {
			t.Fatalf("accepted %q", path)
		}
	}
}

func TestMediaAPIHistoryBatchPreservesOrderAndErrors(t *testing.T) {
	history, err := sqlite.NewChatHistory(t.Context(), filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = history.Close() })
	runtime, center := newMediaAPITest(t, history.Repository())
	record, err := center.ImportBytes(t.Context(), []byte("file"), media.Input{Name: "file.txt", MIMEType: "text/plain"}, media.ImportLimits{})
	if err != nil {
		t.Fatal(err)
	}
	stored := storage.ChatMessage{Platform: "test", ChatID: "chat", PlatformMessageID: "message", Segments: `[{"type":"file","platform_file_id":"file"}]`}
	if err := history.Repository().Append(t.Context(), &stored); err != nil {
		t.Fatal(err)
	}
	row := media.HistoryMessage{ID: stored.ID, Platform: "test", ChatID: "chat", MessageID: "message", Media: []media.PlatformMedia{{Type: "file", PlatformFileID: "file"}}}
	if err := center.AssociateHistory(t.Context(), row, 1, "file", record.Data.ID); err != nil {
		t.Fatal(err)
	}
	requests := []media.HistoryMediaRequest{{Message: row, Index: 1}, {Message: row, Index: 2}, {Message: row, Index: 1}}
	results, err := runtime.GetHistoryMediaBatch(t.Context(), requests, nil, media.HistoryFetchOptions{MaxFetchAttempts: 1})
	if err != nil || len(results) != 3 {
		t.Fatalf("results=%+v %v", results, err)
	}
	want := share.MediaInfo{ID: record.Data.ID, Name: record.Data.Name, MIMEType: record.Data.MIMEType, Size: record.Data.Size}
	if results[0].Err != nil || results[0].Media.Status != operationresult.Success || results[0].Media.Data != want || results[2].Err != nil || results[2].Media.Status != operationresult.Success || results[2].Media.Data != want {
		t.Fatalf("order or cache lost: %+v", results)
	}
	if results[1].Err != nil || results[1].Media.Status != operationresult.InvalidArguments {
		t.Fatalf("item error=%+v", results[1])
	}
	ids, err := runtime.HistoryIDs(t.Context(), row)
	if err != nil || ids[1] != record.Data.ID {
		t.Fatalf("ids=%v err=%v", ids, err)
	}
}
