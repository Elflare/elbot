// inbound_media_test.go 验证入站媒体历史记录与延迟解析。

package telegram

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/platform/share"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

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
	a := newTestAdapter(t, Config{BotToken: "123:secret", APIBaseURL: server.URL, FileBaseURL: server.URL}, history.Repository())

	var captured share.MessageContext
	handled := 0
	handler := func(_ context.Context, _ string, msg share.MessageContext) error {
		captured = msg
		handled++
		return nil
	}
	a.handleMessage(ctx, handler, message{MessageID: 1, Chat: chat{ID: 9, Type: "group"}, Photo: []photoSize{{FileID: "photo-id", FileSize: 5}}}, share.Identity{})
	if calls != 0 || handled != 1 || captured.MediaResolver != a {
		t.Fatalf("eager calls/handler = %d/%#v", calls, captured)
	}
	if captured.Conversation.Source.ConversationID != "9" || captured.Conversation.Source.ScopeID != "group:9" {
		t.Fatalf("conversation source = %+v", captured.Conversation.Source)
	}
	row, err := history.Repository().GetByPlatformMessage(ctx, platformName, "group:9", "1")
	if err != nil {
		t.Fatal(err)
	}
	segments := storage.UnmarshalChatSegments(row.Segments)
	if len(segments) != 1 || segments[0].PlatformFileID != "photo-id" || segments[0].URL != "" || strings.Contains(row.Segments, "secret") {
		t.Fatalf("history = %#v", row)
	}
	source, err := a.ResolveMedia(ctx, captured.Segments[0], 10)
	if err != nil || string(source.Data) != "image" || calls != 2 || source.URL != "" {
		t.Fatalf("resolved = %#v, calls=%d, err=%v", source, calls, err)
	}
	if _, err = a.ResolveMedia(ctx, captured.Segments[0], 3); err == nil || calls != 3 {
		t.Fatalf("declared limit: calls=%d err=%v", calls, err)
	}
	if _, err = a.client.downloadFile(ctx, "photos/a.jpg", 3); err == nil {
		t.Fatal("actual body limit not enforced")
	}
}
