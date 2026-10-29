package tool

import toolshare "elbot/internal/tool/share"

import (
	"bytes"
	"context"
	toolmedia "elbot/internal/tool/media"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/media"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
	"elbot/internal/testutil/mediatest"
	"elbot/internal/tool/builtin"
	"elbot/internal/tool/chat_history"
)

func TestHistoryBridgeProjectsStoredReplyAndOrderedMedia(t *testing.T) {
	ctx, fixture, resolver := newHistoryMediaToolTest(t)
	reply := storage.ChatReply{MessageID: "reply:opaque-id", SenderID: "sender", SenderName: "名字", Text: "quoted"}
	row := storage.ChatMessage{Platform: "p", PlatformScopeID: "s", PlatformMessageID: "opaque/id", SenderID: "author", SenderName: "nickname", Text: "mixed", ReplyToPlatformMessageID: reply.MessageID, Metadata: storage.MarshalChatMetadata(reply), Segments: storage.MarshalChatSegments([]storage.ChatSegment{
		{Type: "text", Text: "before"}, {Type: "file", PlatformFileID: "file"}, {Type: "image", PlatformFileID: "first"}, {Type: "text", Text: "between"}, {Type: "image", PlatformFileID: "second"},
	})}
	if err := fixture.history.Append(ctx, &row); err != nil {
		t.Fatal(err)
	}
	bridge := &chatHistoryBridge{history: fixture.history, platform: resolver, media: fixture.center}
	key := toolshare.MessageKey{Platform: "p", ScopeID: "s", MessageID: row.PlatformMessageID}
	got, err := bridge.GetMessage(ctx, key)
	if err != nil || got.Key != key || got.SenderID != row.SenderID || got.SenderName != row.SenderName || got.Text != row.Text || got.Reply != (toolshare.ReplyContext{MessageID: reply.MessageID, SenderID: reply.SenderID, SenderName: reply.SenderName, Text: reply.Text}) {
		t.Fatalf("projection=%+v err=%v", got, err)
	}
	if len(got.Media) != 3 || got.Media[0].IsImage || !got.Media[1].IsImage || !got.Media[2].IsImage || resolver.calls != 0 {
		t.Fatal("media order/download changed", got.Media, resolver.calls)
	}
	key.ScopeID = "other"
	if _, err := bridge.GetMessage(ctx, key); !errors.Is(err, toolshare.ErrNotFound) {
		t.Fatal("scope/not-found conversion", err)
	}
	for _, tc := range []struct{ source, want error }{
		{fmt.Errorf("wrapped: %w", storage.ErrNotFound), toolshare.ErrNotFound},
		{media.ErrHistoryMediaIndex, toolshare.ErrMediaIndex},
		{media.ErrHistoryFetchLimit, toolshare.ErrFetchLimit},
		{context.Canceled, context.Canceled},
	} {
		if err := historyError(tc.source); !errors.Is(err, tc.want) {
			t.Fatal(tc.source, err)
		}
	}
}

type getMediaResolver struct {
	data  []byte
	calls int
	fail  bool
}

func (r *getMediaResolver) ResolveMedia(_ context.Context, _ string, s media.PlatformMedia, _ int64) (delivery.Source, error) {
	r.calls++
	if r.fail {
		return delivery.Source{}, fmt.Errorf("https://secret-token/private")
	}
	if r.data != nil {
		return delivery.Source{Data: r.data, MIMEType: "image/png"}, nil
	}
	return delivery.Source{Data: []byte(s.PlatformFileID), MIMEType: "image/png"}, nil
}

type historyMediaToolFixture struct {
	builtin.GetMediaTool
	manager     *chat_history.Manager
	history     storage.ChatHistoryRepository
	center      *toolmedia.MediaRuntime
	mediaCenter *media.MediaCenter
}

func newHistoryMediaToolTest(t *testing.T, configure ...func(*config.Config)) (context.Context, historyMediaToolFixture, *getMediaResolver) {
	t.Helper()
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "main.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	history, err := sqlite.NewChatHistory(ctx, filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { history.Close() })
	root := t.TempDir()
	center := mediatest.New(t, media.MediaCenterDeps{Store: store, ChatHistory: history.Repository()}, root, configure...)
	var segments []media.PlatformMedia
	for i := 1; i <= 7; i++ {
		segments = append(segments, media.PlatformMedia{Type: "image", PlatformFileID: fmt.Sprint(i)})
	}
	var storedSegments []map[string]any
	for _, segment := range segments {
		storedSegments = append(storedSegments, map[string]any{"type": segment.Type, "platform_file_id": segment.PlatformFileID})
	}
	encoded, err := json.Marshal(storedSegments)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"1", "2"} {
		if err := history.Repository().Append(ctx, &storage.ChatMessage{Platform: "p", PlatformScopeID: "s", PlatformMessageID: id, SenderID: "u", Text: "pictures", Segments: string(encoded)}); err != nil {
			t.Fatal(err)
		}
	}
	resolver := &getMediaResolver{}
	ctx = contextinfo.WithConversation(ctx, contextinfo.Conversation{Source: contextinfo.Source{Platform: "p", ScopeID: "s"}})
	mediaRuntime := toolmedia.NewMediaRuntime(center, root, mediatest.Publisher(center))
	manager := chat_history.NewManager(&chatHistoryBridge{history: history.Repository(), platform: resolver, media: mediaRuntime}, time.Now)
	return ctx, historyMediaToolFixture{GetMediaTool: builtin.NewGetMediaTool(manager), manager: manager, history: history.Repository(), center: mediaRuntime, mediaCenter: center}, resolver
}

func TestGetMediaLimitCacheDefaultsAndTextOnly(t *testing.T) {
	ctx, tools, resolver := newHistoryMediaToolTest(t)
	result, _, err := callTool(ctx, tools, toolshare.CallRequest{Arguments: json.RawMessage(`{"message_id":["1"],"media_index":[[1,2,3,4,5,6,1]]}`)})
	if err != nil || resolver.calls != 5 || len(result.Segments) != 0 || len(result.Outputs) != 0 || !strings.Contains(result.Content, "达到本次") {
		t.Fatalf("result = %#v err=%v calls=%d", result, err, resolver.calls)
	}
	result, _, err = callTool(ctx, tools, toolshare.CallRequest{Arguments: json.RawMessage(`{"message_id":["#1","2"]}`)})
	if err != nil || resolver.calls != 6 || strings.Count(result.Content, "[图片 media:") != 2 {
		t.Fatalf("defaults = %#v %v calls=%d", result, err, resolver.calls)
	}
	result, _, err = callTool(ctx, tools, toolshare.CallRequest{Arguments: json.RawMessage(`{"message_id":["1"],"media_index":[[1,2,3,4,5,6,7]]}`)})
	if err != nil || resolver.calls != 8 || strings.Contains(result.Content, "上限") {
		t.Fatalf("cache counted = %#v %v calls=%d", result, err, resolver.calls)
	}
}

func TestGetMediaValidationAndFailureBudget(t *testing.T) {
	ctx, tools, resolver := newHistoryMediaToolTest(t)
	for _, raw := range []string{`{}`, `{"message_id":[]}`, `{"message_id":[""]}`, `{"message_id":["1"],"media_index":[]}`, `{"message_id":["1"],"media_index":[[]]}`, `{"message_id":["1"],"media_index":[[0]]}`, `{"message_id":["1"],"media_index":[[-1]]}`} {
		if _, _, err := callTool(ctx, tools, toolshare.CallRequest{Arguments: json.RawMessage(raw)}); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if resolver.calls != 0 {
		t.Fatal("invalid arguments downloaded")
	}
	resolver.fail = true
	result, _, err := callTool(ctx, tools, toolshare.CallRequest{Arguments: json.RawMessage(`{"message_id":["1"],"media_index":[[1,1,2,3,4,5,6,100]]}`)})
	if err != nil || resolver.calls != 5 || strings.Contains(result.Content, "secret-token") || !strings.Contains(result.Content, "序号越界") || !strings.Contains(result.Content, "达到本次") {
		t.Fatalf("failure result = %#v %v calls=%d", result, err, resolver.calls)
	}
	other := contextinfo.WithConversation(ctx, contextinfo.Conversation{Source: contextinfo.Source{Platform: "p", ScopeID: "other"}})
	result, _, err = callTool(other, tools, toolshare.CallRequest{Arguments: json.RawMessage(`{"message_id":["1"]}`)})
	if err != nil || resolver.calls != 5 || !strings.Contains(result.Content, "没有该消息") {
		t.Fatalf("scope leaked: %#v %v", result, err)
	}
}

func TestGetMediaErrorLogSeparators(t *testing.T) {
	for _, args := range []string{
		`{"message_id":["missing"]}`,
		`{"message_id":["missing","other"]}`,
		`{"message_id":["1"],"media_index":[[100]]}`,
		`{"message_id":["missing","1"],"media_index":[[1],[100]]}`,
	} {
		t.Run(args, func(t *testing.T) {
			ctx, tools, _ := newHistoryMediaToolTest(t)
			result, logContent, err := callTool(ctx, tools, toolshare.CallRequest{Arguments: json.RawMessage(args)})
			if err != nil {
				t.Fatal(err)
			}
			if logContent == "" || logContent != result.Content || strings.HasSuffix(logContent, "\n") || strings.Contains(logContent, "\n\n") {
				t.Fatalf("log=%q business=%q", logContent, result.Content)
			}
		})
	}
}

func TestHistoryQueriesShowMediaWithoutDownload(t *testing.T) {
	ctx, tools, resolver := newHistoryMediaToolTest(t)
	search := builtin.NewSearchChatHistoryTool(tools.manager, nil)
	around := builtin.NewGetChatHistoryAroundTool(tools.manager, nil)
	for _, call := range []struct {
		tool toolshare.Tool
		args string
	}{{search, `{}`}, {around, `{"message_id":"1"}`}} {
		result, _, err := callTool(ctx, call.tool, toolshare.CallRequest{Arguments: json.RawMessage(call.args)})
		if err != nil || resolver.calls != 0 || !strings.Contains(result.Content, "1. [图片 media:未下载]") || len(result.Segments) != 0 {
			t.Fatalf("history result = %#v %v", result, err)
		}
	}
	if _, _, err := callTool(ctx, tools, toolshare.CallRequest{Arguments: json.RawMessage(`{"message_id":["1"]}`)}); err != nil {
		t.Fatal(err)
	}
	row, err := tools.history.GetByPlatformMessage(ctx, "p", "s", "1")
	if err != nil {
		t.Fatal(err)
	}
	ids, err := tools.center.HistoryIDs(ctx, historyMediaMessage(*row))
	if err != nil {
		t.Fatal(err)
	}
	result, _, err := callTool(ctx, search, toolshare.CallRequest{Arguments: json.RawMessage(`{}`)})
	if err != nil || resolver.calls != 1 || !strings.Contains(result.Content, "[图片 "+ids[1]+"]") {
		t.Fatalf("downloaded history = %#v %v", result, err)
	}
}

func TestGetMediaReturnsCompressedHistoryID(t *testing.T) {
	ctx, tools, resolver := newHistoryMediaToolTest(t, func(cfg *config.Config) { cfg.Media.LLMImageMaxLength = 32 })
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewNRGBA(image.Rect(0, 0, 80, 40))); err != nil {
		t.Fatal(err)
	}
	resolver.data = data.Bytes()
	request := toolshare.CallRequest{Arguments: json.RawMessage(`{"message_id":["1"]}`)}
	first, _, err := callTool(ctx, tools, request)
	if err != nil {
		t.Fatal(err)
	}
	row, err := tools.history.GetByPlatformMessage(ctx, "p", "s", "1")
	if err != nil {
		t.Fatal(err)
	}
	ids, err := tools.center.HistoryIDs(ctx, historyMediaMessage(*row))
	if err != nil {
		t.Fatal(err)
	}
	stored, item, err := tools.mediaCenter.Read(ctx, ids[1])
	if err != nil {
		t.Fatal(err)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(stored))
	if err != nil || format != "jpeg" || cfg.Width >= 32 || cfg.Height >= 32 || item.MIMEType != "image/jpeg" || !strings.Contains(first.Content, ids[1]) || len(first.Segments) != 0 {
		t.Fatalf("compressed result = %#v, media %#v, dimensions %#v, %v", first, item, cfg, err)
	}
	again, _, err := callTool(ctx, tools, request)
	if err != nil || again.Content != first.Content || resolver.calls != 1 {
		t.Fatalf("cached result = %#v, calls %d, error %v", again, resolver.calls, err)
	}
}

func newHistoryTestManager(repository storage.ChatHistoryRepository) *chat_history.Manager {
	return chat_history.NewManager(&chatHistoryBridge{history: repository}, time.Now)
}
