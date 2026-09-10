package telegram

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/platform"
	"elbot/internal/storage/sqlite"
)

type mediaCaptureHandler struct {
	msg   platform.MessageContext
	calls int
}

func (h *mediaCaptureHandler) HandleMessage(ctx context.Context, _ string) error {
	h.msg, _ = platform.MessageContextFrom(ctx)
	h.calls++
	return nil
}

func TestInboundMediaHistoryAndDeferredResolver(t *testing.T) {
	ctx := context.Background()
	history, err := sqlite.NewChatHistory(ctx, filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer history.Close()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if strings.HasSuffix(r.URL.Path, "getFile") {
			fmt.Fprint(w, `{"ok":true,"result":{"file_id":"photo-id","file_size":5,"file_path":"photos/a.jpg"}}`)
			return
		}
		fmt.Fprint(w, "image")
	}))
	defer server.Close()
	a := New(Config{BotToken: "123:secret", APIBaseURL: server.URL, FileBaseURL: server.URL}, nil, history.Repository(), nil)
	a.client.http = server.Client()
	handler := &mediaCaptureHandler{}
	a.handleMessage(ctx, handler, message{MessageID: 1, Chat: chat{ID: 9, Type: "group"}, Photo: []photoSize{{FileID: "photo-id", FileSize: 5}}})
	if calls != 0 || handler.calls != 1 || handler.msg.MediaResolver != a {
		t.Fatalf("eager calls/handler = %d/%#v", calls, handler)
	}
	row, err := history.Repository().GetByPlatformMessage(ctx, platformName, "group:9", "1")
	if err != nil {
		t.Fatal(err)
	}
	segments := platform.UnmarshalChatSegments(row.Segments)
	if len(segments) != 1 || segments[0].PlatformFileID != "photo-id" || segments[0].URL != "" || strings.Contains(row.Segments, "secret") {
		t.Fatalf("history = %#v", row)
	}
	source, err := a.ResolveMedia(ctx, segments[0], 10)
	if err != nil || string(source.Data) != "image" || calls != 2 || source.URL != "" {
		t.Fatalf("resolved = %#v, calls=%d, err=%v", source, calls, err)
	}
	if _, err = a.ResolveMedia(ctx, segments[0], 3); err == nil || calls != 3 {
		t.Fatalf("declared limit: calls=%d err=%v", calls, err)
	}
	if _, err = a.client.downloadFile(ctx, "photos/a.jpg", 3); err == nil {
		t.Fatal("actual body limit not enforced")
	}
}
