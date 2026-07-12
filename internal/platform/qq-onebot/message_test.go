package qqonebot

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"elbot/internal/delivery"
	"elbot/internal/platform"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

func TestNewFromPlatformConfig(t *testing.T) {
	adapter, err := NewFromPlatformConfig(map[string]any{
		"enabled":          true,
		"ws_url":           "ws://example",
		"trigger_keywords": []any{"芙莉丝"},
	}, nil, nil, nil, nil, nil, t.TempDir(), 100*1024*1024, 60)
	if err != nil {
		t.Fatalf("NewFromPlatformConfig: %v", err)
	}
	if !adapter.Enabled() || adapter.cfg.URL != "ws://example" || len(adapter.cfg.TriggerKeywords) != 1 || adapter.cfg.TriggerKeywords[0] != "芙莉丝" {
		t.Fatalf("adapter config = %#v", adapter.cfg)
	}
}

func TestQQTextPagesKeepsShortText(t *testing.T) {
	pages := qqTextPages("短消息")
	if len(pages) != 1 || pages[0] != "短消息" {
		t.Fatalf("pages = %#v", pages)
	}
}

func TestQQTextPagesSplitsLongText(t *testing.T) {
	pages := qqTextPages(strings.Repeat("a", qqTextPageRunes*2+1))
	if len(pages) != 3 {
		t.Fatalf("page count = %d", len(pages))
	}
	if !strings.HasSuffix(pages[0], "……（1/3）") || !strings.HasSuffix(pages[1], "……（2/3）") || !strings.HasSuffix(pages[2], "……（3/3）") {
		t.Fatalf("pages = %#v", pages)
	}
	if got := len([]rune(strings.TrimSuffix(pages[0], "……（1/3）"))); got != qqTextPageRunes {
		t.Fatalf("first page body runes = %d", got)
	}
	if got := len([]rune(strings.TrimSuffix(pages[2], "……（3/3）"))); got != 1 {
		t.Fatalf("last page body runes = %d", got)
	}
}

func TestQQTextPagesSplitsChineseRunes(t *testing.T) {
	pages := qqTextPages(strings.Repeat("娅", qqTextPageRunes) + "芙")
	if len(pages) != 2 {
		t.Fatalf("page count = %d", len(pages))
	}
	if !strings.HasPrefix(pages[1], "芙……（2/2）") {
		t.Fatalf("second page = %q", pages[1])
	}
}

func TestNormalizeArrayMessage(t *testing.T) {
	msg := normalizeMessage([]byte(`[

		{"type":"at","data":{"qq":"1000"}},
		{"type":"text","data":{"text":"  hello  "}},
		{"type":"reply","data":{"id":"42"}},
		{"type":"image","data":{"file":"a.jpg"}}
	]`), "", 1000)
	if msg.ReplyID != "42" || msg.Text != "hello [图片]" {
		t.Fatalf("message = %#v", msg)
	}
	if len(msg.Mentions) != 1 || msg.Mentions[0].UserID != "1000" {
		t.Fatalf("mentions = %#v", msg.Mentions)
	}
	if len(msg.Segments) != 2 || msg.Segments[0].Type != "text" || msg.Segments[1].Type != "image" || msg.Segments[1].Name != "a.jpg" {
		t.Fatalf("segments = %#v", msg.Segments)
	}
}

func TestNormalizeImageFileURL(t *testing.T) {
	msg := normalizeMessage([]byte(`[{"type":"image","data":{"file":"https://example.com/a.jpg"}}]`), "", 1000)
	if len(msg.Segments) != 1 || msg.Segments[0].URL != "https://example.com/a.jpg" {
		t.Fatalf("segments = %#v", msg.Segments)
	}
}

func TestImageFileDataURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.png")
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	if err := os.WriteFile(path, png, 0o600); err != nil {
		t.Fatalf("write image: %v", err)
	}
	url, err := imageFileDataURL(path)
	if err != nil {
		t.Fatalf("imageFileDataURL: %v", err)
	}
	if !strings.HasPrefix(url, "data:image/png;base64,") {
		t.Fatalf("url = %q", url)
	}
}

func TestNormalizeArrayImageAndFileSegments(t *testing.T) {
	msg := normalizeMessage([]byte(`[
		{"type":"text","data":{"text":"看"}},
		{"type":"image","data":{"file":"a.jpg","url":"https://example.com/a.jpg"}},
		{"type":"record","data":{"file":"v.amr"}}
	]`), "", 1000)
	if msg.Text != "看[图片][语音]" {
		t.Fatalf("text = %q", msg.Text)
	}
	if len(msg.Segments) != 3 || msg.Segments[1].Type != "image" || msg.Segments[1].URL != "https://example.com/a.jpg" || msg.Segments[2].Type != "file" || msg.Segments[2].Text != "语音" {
		t.Fatalf("segments = %#v", msg.Segments)
	}
}

func TestNormalizeStringifiedArrayImageIgnoresRawMessage(t *testing.T) {
	raw := []byte(`"[{\"type\":\"image\",\"data\":{\"file\":\"E50BAC9EAA237E638057A4C662990635.jpg\",\"subType\":1,\"url\":\"https://multimedia.nt.qq.com.cn/download?appid=1406&fileid=abc&spec=0&rkey=xyz\",\"file_size\":\"1349\"}}]"`)
	msg := normalizeMessage(raw, `raw fallback must not be used`, 1000)
	if msg.Text != "[图片]" {
		t.Fatalf("text = %q", msg.Text)
	}
	if len(msg.Segments) != 1 || msg.Segments[0].Type != "image" {
		t.Fatalf("segments = %#v", msg.Segments)
	}
	if msg.Segments[0].URL != "https://multimedia.nt.qq.com.cn/download?appid=1406&fileid=abc&spec=0&rkey=xyz" {
		t.Fatalf("url = %q", msg.Segments[0].URL)
	}
	if msg.Segments[0].Name != "E50BAC9EAA237E638057A4C662990635.jpg" {
		t.Fatalf("name = %q", msg.Segments[0].Name)
	}
}

func TestNormalizePlainTextDoesNotParseMarkup(t *testing.T) {
	msg := normalizeMessage(nil, `[image file=a.jpg url=https://example.com/a.jpg]`, 1000)
	if msg.Text != `[image file=a.jpg url=https://example.com/a.jpg]` {
		t.Fatalf("text = %q", msg.Text)
	}
	if len(msg.Segments) != 1 || msg.Segments[0].Type != "text" {
		t.Fatalf("segments = %#v", msg.Segments)
	}
}

func TestPrepareInboundAttachmentsSavesFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("test file"))
	}))
	defer server.Close()

	adapter := New(Config{AttachmentDir: t.TempDir(), MaxReceiveFileBytes: 1024, DownloadTimeoutSecs: 60}, nil, nil, nil)
	prepared := adapter.prepareInboundAttachments(context.Background(), []platform.MessageSegment{{Type: platform.SegmentFile, Text: "文件", URL: server.URL + "/file", Name: "test.txt"}})

	if len(prepared.Saved) != 1 {
		t.Fatalf("saved len = %d, want 1", len(prepared.Saved))
	}
	if filepath.Base(prepared.Saved[0].Path) != "test.txt" {
		t.Fatalf("saved path = %q, want test.txt", prepared.Saved[0].Path)
	}
	data, err := os.ReadFile(prepared.Saved[0].Path)
	if err != nil {
		t.Fatalf("read saved file: %v", err)
	}
	if string(data) != "test file" {
		t.Fatalf("saved data = %q, want test file", string(data))
	}
	if len(prepared.Segments) != 1 || prepared.Segments[0].Type != platform.SegmentFile || prepared.Segments[0].Name != prepared.Saved[0].Path || prepared.Segments[0].MIMEType != "text/plain" {
		t.Fatalf("segments = %#v", prepared.Segments)
	}
}

func TestHandleEventPureSuperadminFileSendsSavedNotice(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("test file"))
	}))
	defer server.Close()

	transport := newTestTransport(t, func(req request) response {
		if req.Action != "send_private_msg" {
			t.Fatalf("action = %q", req.Action)
		}
		text, _ := req.Params["message"].(string)
		if !strings.Contains(text, "已保存附件：test.txt") || !strings.Contains(text, "路径：") {
			t.Fatalf("notice = %q", text)
		}
		return response{Status: "ok", Data: []byte(`{"message_id":99}`), Echo: req.Echo}
	})
	adapter := New(Config{Enabled: true, URL: transport.URL, AttachmentDir: t.TempDir(), MaxReceiveFileBytes: 1024, DownloadTimeoutSecs: 60, Superadmins: []string{"1"}}, nil, nil, nil)
	adapter.transport = transport
	handler := &captureHandler{}

	adapter.handleEvent(context.Background(), handler, Event{MessageType: "private", SelfID: 1000, UserID: 1, MessageID: 7, Message: []byte(fmt.Sprintf(`[{"type":"file","data":{"file":"test.txt","url":%q}}]`, server.URL+"/file"))})

	if handler.count != 0 {
		t.Fatalf("handler count = %d, want 0", handler.count)
	}
}

func TestHandleEventSuperadminTextAndFileReachesHandler(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("test file"))
	}))
	defer server.Close()

	adapter := New(Config{Enabled: true, AttachmentDir: t.TempDir(), MaxReceiveFileBytes: 1024, DownloadTimeoutSecs: 60, Superadmins: []string{"1"}}, nil, nil, nil)
	handler := &captureHandler{}
	adapter.handleEvent(context.Background(), handler, Event{MessageType: "private", SelfID: 1000, UserID: 1, MessageID: 7, Message: []byte(fmt.Sprintf(`[{"type":"text","data":{"text":"看看"}},{"type":"file","data":{"file":"test.txt","url":%q}}]`, server.URL+"/file"))})

	if handler.count != 1 || handler.text != "看看[文件]" {
		t.Fatalf("handler count/text = %d/%q", handler.count, handler.text)
	}
	msgCtx, ok := platform.MessageContextFrom(handler.ctx)
	if !ok {
		t.Fatal("missing message context")
	}
	if len(msgCtx.Segments) != 2 || msgCtx.Segments[1].Type != platform.SegmentFile || !filepath.IsAbs(msgCtx.Segments[1].Name) {
		t.Fatalf("segments = %#v", msgCtx.Segments)
	}
}

func TestHandleEventPureSuperadminTooLargeFileSendsNotice(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("file_size over limit should not be downloaded")
	}))
	defer server.Close()

	transport := newTestTransport(t, func(req request) response {
		if req.Action != "send_private_msg" {
			t.Fatalf("action = %q", req.Action)
		}
		text, _ := req.Params["message"].(string)
		if !strings.Contains(text, "文件过大，不会保存到服务器：big.txt") {
			t.Fatalf("notice = %q", text)
		}
		return response{Status: "ok", Data: []byte(`{"message_id":100}`), Echo: req.Echo}
	})
	adapter := New(Config{Enabled: true, URL: transport.URL, AttachmentDir: t.TempDir(), MaxReceiveFileBytes: 3, DownloadTimeoutSecs: 60, Superadmins: []string{"1"}}, nil, nil, nil)
	adapter.transport = transport
	handler := &captureHandler{}

	adapter.handleEvent(context.Background(), handler, Event{MessageType: "private", SelfID: 1000, UserID: 1, MessageID: 7, Message: []byte(fmt.Sprintf(`[{"type":"file","data":{"file":"big.txt","url":%q,"file_size":"9"}}]`, server.URL+"/file"))})

	if handler.count != 0 {
		t.Fatalf("handler count = %d, want 0", handler.count)
	}
}

func TestHandleEventPrivateNonSuperadminFileDoesNotSave(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("non-superadmin file should not be downloaded")
	}))
	defer server.Close()

	dir := t.TempDir()
	adapter := New(Config{Enabled: true, AttachmentDir: dir, MaxReceiveFileBytes: 1024, DownloadTimeoutSecs: 60, Superadmins: []string{"2"}}, nil, nil, nil)
	handler := &captureHandler{}
	adapter.handleEvent(context.Background(), handler, Event{MessageType: "private", SelfID: 1000, UserID: 1, MessageID: 7, Message: []byte(fmt.Sprintf(`[{"type":"file","data":{"file":"test.txt","url":%q}}]`, server.URL+"/file"))})

	if handler.count != 0 {
		t.Fatalf("handler count = %d, want 0", handler.count)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read attachment dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("attachment dir entries = %d, want 0", len(entries))
	}
}

func TestHandleEventGroupFileDoesNotSave(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("group file should not be downloaded")
	}))
	defer server.Close()

	dir := t.TempDir()
	adapter := New(Config{Enabled: true, AttachmentDir: dir, MaxReceiveFileBytes: 1024, DownloadTimeoutSecs: 60, Superadmins: []string{"1"}}, nil, nil, nil)
	handler := &captureHandler{}
	adapter.handleEvent(context.Background(), handler, Event{MessageType: "group", SelfID: 1000, UserID: 1, GroupID: 9, MessageID: 7, Message: []byte(fmt.Sprintf(`[{"type":"at","data":{"qq":"1000"}},{"type":"file","data":{"file":"test.txt","url":%q}}]`, server.URL+"/file"))})

	if handler.count != 0 {
		t.Fatalf("handler count = %d, want 0", handler.count)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read attachment dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("attachment dir entries = %d, want 0", len(entries))
	}
}

func TestHandleEventSuperadminFileWithoutURLUsesGetFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("test file"))
	}))
	defer server.Close()

	call := 0
	transport := newTestTransport(t, func(req request) response {
		call++
		switch req.Action {
		case "get_file":
			if req.Params["file"] != "test.txt" || req.Params["download"] != true {
				t.Fatalf("get_file params = %#v", req.Params)
			}
			return response{Status: "ok", Data: []byte(fmt.Sprintf(`{"file":"C:\\QQ\\test.txt","url":%q,"file_size":"9","file_name":"test.txt"}`, server.URL+"/file")), Echo: req.Echo}
		case "send_private_msg":
			text, _ := req.Params["message"].(string)
			if !strings.Contains(text, "已保存附件：test.txt") || !strings.Contains(text, "路径：") {
				t.Fatalf("notice = %q", text)
			}
			return response{Status: "ok", Data: []byte(`{"message_id":101}`), Echo: req.Echo}
		default:
			t.Fatalf("action = %q", req.Action)
		}
		return response{}
	})
	dir := t.TempDir()
	adapter := New(Config{Enabled: true, URL: transport.URL, AttachmentDir: dir, MaxReceiveFileBytes: 1024, DownloadTimeoutSecs: 60, Superadmins: []string{"1"}}, nil, nil, nil)
	adapter.transport = transport
	handler := &captureHandler{}
	adapter.handleEvent(context.Background(), handler, Event{MessageType: "private", SelfID: 1000, UserID: 1, MessageID: 7, Message: []byte(`[{"type":"file","data":{"file":"test.txt","url":"","file_id":"id-1","path":"","file_size":"1"}}]`)})

	if call != 2 {
		t.Fatalf("call = %d, want 2", call)
	}
	if handler.count != 0 {
		t.Fatalf("handler count = %d, want 0", handler.count)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read attachment dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "test.txt" {
		t.Fatalf("attachment dir entries = %#v", entries)
	}
}

func TestHandleEventSuperadminFileWithoutURLUsesGetFilePath(t *testing.T) {
	call := 0
	transport := newTestTransport(t, func(req request) response {
		call++
		switch req.Action {
		case "get_file":
			return response{Status: "ok", Data: []byte(`{"file":"C:\\Users\\Administrator\\Downloads\\test (1).txt","url":"","file_size":"1","file_name":"test.txt"}`), Echo: req.Echo}
		case "send_private_msg":
			text, _ := req.Params["message"].(string)
			if !strings.Contains(text, "已接收附件：test.txt") || !strings.Contains(text, `OneBot 本地路径：C:\Users\Administrator\Downloads\test (1).txt`) || !strings.Contains(text, "如果 OneBot 和 ElBot 不在同一服务器") {
				t.Fatalf("notice = %q", text)
			}
			return response{Status: "ok", Data: []byte(`{"message_id":102}`), Echo: req.Echo}
		default:
			t.Fatalf("action = %q", req.Action)
		}
		return response{}
	})
	adapter := New(Config{Enabled: true, URL: transport.URL, AttachmentDir: t.TempDir(), MaxReceiveFileBytes: 1024, DownloadTimeoutSecs: 60, Superadmins: []string{"1"}}, nil, nil, nil)
	adapter.transport = transport
	handler := &captureHandler{}
	adapter.handleEvent(context.Background(), handler, Event{MessageType: "private", SelfID: 1000, UserID: 1, MessageID: 7, Message: []byte(`[{"type":"file","data":{"file":"test.txt","url":"","file_size":"1"}}]`)})

	if call != 2 {
		t.Fatalf("call = %d, want 2", call)
	}
	if handler.count != 0 {
		t.Fatalf("handler count = %d, want 0", handler.count)
	}
}

func TestHandleEventSuperadminFileWithoutURLTooLargeAfterGetFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("file_size from get_file over limit should not be downloaded")
	}))
	defer server.Close()

	transport := newTestTransport(t, func(req request) response {
		switch req.Action {
		case "get_file":
			return response{Status: "ok", Data: []byte(fmt.Sprintf(`{"file":"C:\\QQ\\big.txt","url":%q,"file_size":"9","file_name":"big.txt"}`, server.URL+"/file")), Echo: req.Echo}
		case "send_private_msg":
			text, _ := req.Params["message"].(string)
			if !strings.Contains(text, "文件过大，不会保存到服务器：big.txt") {
				t.Fatalf("notice = %q", text)
			}
			return response{Status: "ok", Data: []byte(`{"message_id":103}`), Echo: req.Echo}
		default:
			t.Fatalf("action = %q", req.Action)
		}
		return response{}
	})
	adapter := New(Config{Enabled: true, URL: transport.URL, AttachmentDir: t.TempDir(), MaxReceiveFileBytes: 3, DownloadTimeoutSecs: 60, Superadmins: []string{"1"}}, nil, nil, nil)
	adapter.transport = transport
	handler := &captureHandler{}
	adapter.handleEvent(context.Background(), handler, Event{MessageType: "private", SelfID: 1000, UserID: 1, MessageID: 7, Message: []byte(`[{"type":"file","data":{"file":"big.txt","url":""}}]`)})

	if handler.count != 0 {
		t.Fatalf("handler count = %d, want 0", handler.count)
	}
}

func TestHandleEventDeliversPlainGroupMessage(t *testing.T) {
	adapter := New(Config{Enabled: true, URL: "ws://127.0.0.1:6700/", TriggerKeywords: []string{"芙莉丝"}}, nil, nil, nil)
	handler := &captureHandler{}

	adapter.handleEvent(context.Background(), handler, Event{MessageType: "group", SelfID: 1000, UserID: 1, GroupID: 9, RawMessage: "hello"})

	if handler.count != 1 || handler.text != "hello" {
		t.Fatalf("handler count/text = %d/%q", handler.count, handler.text)
	}
	msgCtx, ok := platform.MessageContextFrom(handler.ctx)
	if !ok {
		t.Fatal("missing message context")
	}
	if msgCtx.ConversationKind != platform.ConversationGroup || msgCtx.RawText != "hello" {
		t.Fatalf("message context = %#v", msgCtx)
	}
}

func TestHandleEventKeepsTriggerKeywordForUpperLayers(t *testing.T) {
	adapter := New(Config{Enabled: true, URL: "ws://127.0.0.1:6700/", TriggerKeywords: []string{"芙莉丝"}}, nil, nil, nil)
	handler := &captureHandler{}
	adapter.handleEvent(context.Background(), handler, Event{MessageType: "group", SelfID: 1000, UserID: 1, GroupID: 9, RawMessage: "芙莉丝，你好"})
	if handler.text != "芙莉丝，你好" {
		t.Fatalf("handled text = %q", handler.text)
	}
}

func TestHandleEventAtUsesGroupMemberCard(t *testing.T) {
	transport := newTestTransport(t, func(req request) response {
		if req.Action != "get_group_member_info" {
			t.Fatalf("action = %q", req.Action)
		}
		return response{Status: "ok", Data: []byte(`{"user_id":2,"card":"群昵称","nickname":"普通昵称"}`), Echo: req.Echo}
	})
	adapter := New(Config{Enabled: true, URL: transport.URL}, nil, nil, nil)
	adapter.transport = transport
	handler := &captureHandler{}

	adapter.handleEvent(context.Background(), handler, Event{MessageType: "group", SelfID: 1000, UserID: 1, GroupID: 9, MessageID: 7, Message: []byte(`[{"type":"text","data":{"text":"/status "}},{"type":"at","data":{"qq":"2"}}]`)})

	if handler.text != "/status [at 群昵称 qq:2]" {
		t.Fatalf("handler text = %q", handler.text)
	}
	msgCtx, ok := platform.MessageContextFrom(handler.ctx)
	if !ok {
		t.Fatal("missing message context")
	}
	if len(msgCtx.Segments) != 2 || msgCtx.Segments[1].Text != "[at 群昵称 qq:2]" {
		t.Fatalf("segments = %#v", msgCtx.Segments)
	}
}

func TestHandleEventAtFallsBackToNickname(t *testing.T) {
	transport := newTestTransport(t, func(req request) response {
		if req.Action != "get_group_member_info" {
			t.Fatalf("action = %q", req.Action)
		}
		return response{Status: "ok", Data: []byte(`{"user_id":2,"nickname":"普通昵称"}`), Echo: req.Echo}
	})
	adapter := New(Config{Enabled: true, URL: transport.URL}, nil, nil, nil)
	adapter.transport = transport
	handler := &captureHandler{}

	adapter.handleEvent(context.Background(), handler, Event{MessageType: "group", SelfID: 1000, UserID: 1, GroupID: 9, MessageID: 7, Message: []byte(`[{"type":"text","data":{"text":"/status "}},{"type":"at","data":{"qq":"2"}}]`)})

	if handler.text != "/status [at 普通昵称 qq:2]" {
		t.Fatalf("handler text = %q", handler.text)
	}
}

type captureHandler struct {
	ctx   context.Context
	text  string
	count int
}

func (h *captureHandler) HandleMessage(ctx context.Context, text string) error {
	h.ctx = ctx
	h.text = text
	h.count++
	return nil
}

func TestForkableReferenceMessageIDRequiresOwnAssistantSession(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "elbot.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	own := &storage.Session{OwnerID: "qqonebot:1", Platform: "qqonebot", PlatformScopeID: "group:9", Mode: storage.SessionModeWork, Status: storage.SessionStatusActive, Title: "own"}
	other := &storage.Session{OwnerID: "qqonebot:2", Platform: "qqonebot", PlatformScopeID: "group:9", Mode: storage.SessionModeWork, Status: storage.SessionStatusActive, Title: "other"}
	for _, session := range []*storage.Session{own, other} {
		if err := store.Sessions().Create(ctx, session); err != nil {
			t.Fatalf("create session: %v", err)
		}
	}
	base := storage.Now()
	firstAssistant := &storage.Message{SessionID: own.ID, Role: storage.RoleAssistant, Content: "first answer", CreatedAt: base}
	latestAssistant := &storage.Message{SessionID: own.ID, Role: storage.RoleAssistant, Content: "latest answer", CreatedAt: base.Add(3 * time.Second)}
	otherAssistant := &storage.Message{SessionID: other.ID, Role: storage.RoleAssistant, Content: "other answer", CreatedAt: base.Add(time.Second)}
	ownUser := &storage.Message{SessionID: own.ID, Role: storage.RoleUser, Content: "own user", CreatedAt: base.Add(2 * time.Second)}
	for _, msg := range []*storage.Message{firstAssistant, otherAssistant, ownUser, latestAssistant} {
		if err := store.Messages().Append(ctx, msg); err != nil {
			t.Fatalf("append message: %v", err)
		}
	}
	maps := []storage.PlatformMessageMap{
		{Platform: "qqonebot", PlatformScopeID: "group:9", PlatformMessageID: "first-assistant", MessageID: firstAssistant.ID, SessionID: own.ID},
		{Platform: "qqonebot", PlatformScopeID: "group:9", PlatformMessageID: "latest-assistant", MessageID: latestAssistant.ID, SessionID: own.ID},
		{Platform: "qqonebot", PlatformScopeID: "group:9", PlatformMessageID: "other-assistant", MessageID: otherAssistant.ID, SessionID: other.ID},
		{Platform: "qqonebot", PlatformScopeID: "group:9", PlatformMessageID: "own-user", MessageID: ownUser.ID, SessionID: own.ID},
	}
	for _, mapping := range maps {
		if err := store.Messages().MapPlatformMessage(ctx, mapping); err != nil {
			t.Fatalf("map platform message: %v", err)
		}
	}

	adapter := New(Config{Enabled: true, URL: "ws://127.0.0.1:6700/"}, store, nil, nil)

	handler := &captureHandler{}
	adapter.handleEvent(ctx, handler, Event{MessageType: "group", SelfID: 1000, UserID: 1, GroupID: 9, Message: []byte(`[{"type":"reply","data":{"id":"first-assistant"}},{"type":"text","data":{"text":"继续"}}]`)})
	msgCtx, ok := platform.MessageContextFrom(handler.ctx)
	if !ok {
		t.Fatal("missing message context")
	}
	if msgCtx.ForkFromMessageID != firstAssistant.ID {
		t.Fatalf("historical assistant fork id = %q, want %q", msgCtx.ForkFromMessageID, firstAssistant.ID)
	}
	if handler.text != "继续" {
		t.Fatalf("historical assistant reference text = %q, want original", handler.text)
	}

	handler = &captureHandler{}
	adapter.handleEvent(ctx, handler, Event{MessageType: "group", SelfID: 1000, UserID: 1, GroupID: 9, Message: []byte(`[{"type":"reply","data":{"id":"latest-assistant"}},{"type":"text","data":{"text":"继续"}}]`)})
	msgCtx, ok = platform.MessageContextFrom(handler.ctx)
	if !ok {
		t.Fatal("missing message context")
	}
	if msgCtx.ForkFromMessageID != "" {
		t.Fatalf("latest assistant should continue current conversation, got fork id %q", msgCtx.ForkFromMessageID)
	}
	if handler.text != "继续" {
		t.Fatalf("latest assistant reference text = %q, want direct continuation", handler.text)
	}

	handler = &captureHandler{}
	adapter.handleEvent(ctx, handler, Event{MessageType: "group", SelfID: 1000, UserID: 1, GroupID: 9, Message: []byte(`[{"type":"reply","data":{"id":"other-assistant"}},{"type":"text","data":{"text":"继续"}}]`)})
	msgCtx, ok = platform.MessageContextFrom(handler.ctx)
	if !ok {
		t.Fatal("missing message context")
	}
	if handler.text != "继续" {
		t.Fatalf("other assistant current text = %q, want current", handler.text)
	}
	if msgCtx.ContextText != "[引用：bot]：other answer\n\n继续" {
		t.Fatalf("other assistant context text = %q", msgCtx.ContextText)
	}
	if msgCtx.Reply.MessageID != "other-assistant" || msgCtx.Reply.Text != "other answer" {
		t.Fatalf("other assistant reply = %#v", msgCtx.Reply)
	}

	handler = &captureHandler{}
	adapter.cfg.TriggerKeywords = []string{"芙莉丝"}
	adapter.handleEvent(ctx, handler, Event{MessageType: "group", SelfID: 1000, UserID: 1, GroupID: 9, Message: []byte(`[{"type":"reply","data":{"id":"own-user"}},{"type":"text","data":{"text":"芙莉丝 继续"}}]`)})
	msgCtx, ok = platform.MessageContextFrom(handler.ctx)
	if !ok {
		t.Fatal("missing message context")
	}
	if handler.text != "芙莉丝 继续" {
		t.Fatalf("user current text = %q, want current", handler.text)
	}
	if msgCtx.ContextText != "[引用]：own user\n\n芙莉丝 继续" {
		t.Fatalf("user context text = %q", msgCtx.ContextText)
	}
	if msgCtx.Reply.MessageID != "own-user" || msgCtx.Reply.Text != "own user" {
		t.Fatalf("user reply = %#v", msgCtx.Reply)
	}
}

func TestFinalMessageSegmentsIncludesReferenceImage(t *testing.T) {
	current := []platform.MessageSegment{{Type: platform.SegmentText, Text: "看这个"}}
	referenced := []platform.MessageSegment{{Type: platform.SegmentText, Text: "[图片]"}, {Type: platform.SegmentImage, URL: "https://example.com/a.jpg", Name: "a.jpg"}}

	segments := finalMessageSegments("[引用：用户(qq:1)]：[图片]\n\n看这个", current, referenced)
	if len(segments) != 2 {
		t.Fatalf("segments = %#v", segments)
	}
	if segments[0].Type != platform.SegmentText || !strings.Contains(segments[0].Text, "[引用：用户(qq:1)]：[图片]") {
		t.Fatalf("text segment = %#v", segments[0])
	}
	if segments[1].Type != platform.SegmentImage || segments[1].URL != "https://example.com/a.jpg" || segments[1].Name != "a.jpg" {
		t.Fatalf("image segment = %#v", segments[1])
	}
}

func TestOutputSegments(t *testing.T) {
	segments, err := outputSegments(delivery.Emoticon("178", "滑稽", ""))
	if err != nil {
		t.Fatalf("outputSegments emoticon: %v", err)
	}
	if len(segments) != 1 || segments[0].Type != "face" || segmentDataString(segments[0].Data, "id") != "178" {
		t.Fatalf("emoticon segments = %#v", segments)
	}

	segments, err = outputSegments(delivery.At("123456"))
	if err != nil {
		t.Fatalf("outputSegments at: %v", err)
	}
	if len(segments) != 1 || segments[0].Type != "at" || segments[0].Data["qq"] != "123456" {
		t.Fatalf("at segments = %#v", segments)
	}
}

func TestHandleEventFillsReplyToSenderID(t *testing.T) {
	transport := newTestTransport(t, func(req request) response {
		switch req.Action {
		case "get_msg":
			return response{Status: "ok", Data: []byte(`{"user_id":1000,"message":[]}`), Echo: req.Echo}
		default:
			t.Fatalf("action = %q", req.Action)
		}
		return response{}
	})
	adapter := New(Config{Enabled: true, URL: transport.URL}, nil, nil, nil)
	adapter.transport = transport
	handler := &captureHandler{}

	adapter.handleEvent(context.Background(), handler, Event{MessageType: "group", SelfID: 1000, UserID: 1, GroupID: 9, MessageID: 7, Message: []byte(`[{"type":"reply","data":{"id":"77"}},{"type":"text","data":{"text":"继续"}}]`)})

	msgCtx, ok := platform.MessageContextFrom(handler.ctx)
	if !ok {
		t.Fatal("missing message context")
	}
	if msgCtx.ReplyToSenderID != "1000" {
		t.Fatalf("reply sender = %q", msgCtx.ReplyToSenderID)
	}
}

func TestWithReferenceUsesGetMessageImageWhenStoreHasText(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "elbot.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	session := &storage.Session{OwnerID: "qqonebot:1", Platform: "qqonebot", PlatformScopeID: "group:9", Mode: storage.SessionModeWork, Status: storage.SessionStatusActive, Title: "s"}
	if err := store.Sessions().Create(ctx, session); err != nil {
		t.Fatalf("create session: %v", err)
	}
	assistant := &storage.Message{SessionID: session.ID, Role: storage.RoleAssistant, Content: "stored answer"}
	if err := store.Messages().Append(ctx, assistant); err != nil {
		t.Fatalf("append assistant: %v", err)
	}
	if err := store.Messages().MapPlatformMessage(ctx, storage.PlatformMessageMap{Platform: "qqonebot", PlatformScopeID: "group:9", PlatformMessageID: "77", MessageID: assistant.ID, SessionID: session.ID}); err != nil {
		t.Fatalf("map platform message: %v", err)
	}

	transport := newTestTransport(t, func(req request) response {
		return response{Status: "ok", Data: []byte(`{"user_id":2,"sender":{"nickname":"用户"},"message":[{"type":"image","data":{"file":"a.jpg","url":"https://example.com/a.jpg"}}]}`), Echo: req.Echo}
	})
	adapter := New(Config{Enabled: true, URL: transport.URL}, store, nil, nil)
	adapter.transport = transport

	ref, ok := adapter.referenceFetcher(Event{MessageType: "group", SelfID: 1000, GroupID: 9})(ctx, "77")
	if !ok {
		t.Fatal("missing reference")
	}
	if ref.Label != "引用：用户(qq:2)" {
		t.Fatalf("reference label = %q", ref.Label)
	}
	if len(ref.Segments) != 1 || ref.Segments[0].Type != platform.SegmentImage || ref.Segments[0].URL != "https://example.com/a.jpg" {
		t.Fatalf("reference segments = %#v", ref.Segments)
	}
}

func newTestTransport(t *testing.T, handle func(request) response) *Transport {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")
		for {
			var req request
			if err := wsjson.Read(r.Context(), conn, &req); err != nil {
				return
			}
			resp := handle(req)
			if resp.Status == "" {
				resp.Status = "ok"
			}
			if resp.Retcode == 0 {
				resp.Retcode = 0
			}
			if resp.Echo == "" {
				resp.Echo = req.Echo
			}
			if err := wsjson.Write(r.Context(), conn, resp); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	transport := &Transport{URL: "ws" + strings.TrimPrefix(server.URL, "http"), Timeout: time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	if err := transport.Connect(ctx); err != nil {
		t.Fatalf("connect transport: %v", err)
	}
	go func() {
		for {
			if _, err := transport.Read(ctx); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		cancel()
		transport.Close(websocket.StatusNormalClosure, "test done")
	})
	return transport
}
