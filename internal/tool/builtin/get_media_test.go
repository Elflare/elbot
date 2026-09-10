package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/delivery"
	"elbot/internal/media"
	"elbot/internal/platform"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
	"elbot/internal/tool"
)

type getMediaResolver struct {
	calls int
	fail  bool
}

func (r *getMediaResolver) ResolveMedia(_ context.Context, s platform.MessageSegment, _ int64) (delivery.Source, error) {
	r.calls++
	if r.fail {
		return delivery.Source{}, fmt.Errorf("https://secret-token/private")
	}
	return delivery.Source{Data: []byte(s.PlatformFileID), MIMEType: "image/png"}, nil
}

func newHistoryMediaToolTest(t *testing.T) (context.Context, GetMediaTool, *getMediaResolver) {
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
	center := media.NewManager(store, root, &media.LocalBackend{Root: root})
	center.History = history.Repository()
	var segments []platform.MessageSegment
	for i := 1; i <= 7; i++ {
		segments = append(segments, platform.MessageSegment{Type: platform.SegmentImage, PlatformFileID: fmt.Sprint(i)})
	}
	for _, id := range []string{"1", "2"} {
		if err := history.Repository().Append(ctx, &storage.ChatMessage{Platform: "p", PlatformScopeID: "s", PlatformMessageID: id, SenderID: "u", Text: "pictures", Segments: platform.MarshalChatSegments(segments)}); err != nil {
			t.Fatal(err)
		}
	}
	resolver := &getMediaResolver{}
	ctx = platform.WithMessageContext(ctx, platform.MessageContext{Platform: "p", ScopeID: "s", MediaResolver: resolver})
	return ctx, NewGetMediaTool(history.Repository(), center), resolver
}

func TestGetMediaLimitCacheDefaultsAndTextOnly(t *testing.T) {
	ctx, tools, resolver := newHistoryMediaToolTest(t)
	result, err := tools.Call(ctx, tool.CallRequest{Arguments: json.RawMessage(`{"message_id":["1"],"media_index":[[1,2,3,4,5,6,1]]}`)})
	if err != nil || resolver.calls != 5 || len(result.Segments) != 0 || len(result.Outputs) != 0 || !strings.Contains(result.Content, "达到本次") {
		t.Fatalf("result = %#v err=%v calls=%d", result, err, resolver.calls)
	}
	result, err = tools.Call(ctx, tool.CallRequest{Arguments: json.RawMessage(`{"message_id":["#1","2"]}`)})
	if err != nil || resolver.calls != 6 || strings.Count(result.Content, "[图片 media:") != 2 {
		t.Fatalf("defaults = %#v %v calls=%d", result, err, resolver.calls)
	}
	result, err = tools.Call(ctx, tool.CallRequest{Arguments: json.RawMessage(`{"message_id":["1"],"media_index":[[1,2,3,4,5,6,7]]}`)})
	if err != nil || resolver.calls != 8 || strings.Contains(result.Content, "上限") {
		t.Fatalf("cache counted = %#v %v calls=%d", result, err, resolver.calls)
	}
}

func TestGetMediaValidationAndFailureBudget(t *testing.T) {
	ctx, tools, resolver := newHistoryMediaToolTest(t)
	for _, raw := range []string{`{}`, `{"message_id":[]}`, `{"message_id":[""]}`, `{"message_id":["1"],"media_index":[]}`, `{"message_id":["1"],"media_index":[[]]}`, `{"message_id":["1"],"media_index":[[0]]}`, `{"message_id":["1"],"media_index":[[-1]]}`} {
		if _, err := tools.Call(ctx, tool.CallRequest{Arguments: json.RawMessage(raw)}); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if resolver.calls != 0 {
		t.Fatal("invalid arguments downloaded")
	}
	resolver.fail = true
	result, err := tools.Call(ctx, tool.CallRequest{Arguments: json.RawMessage(`{"message_id":["1"],"media_index":[[1,1,2,3,4,5,6,100]]}`)})
	if err != nil || resolver.calls != 5 || strings.Contains(result.Content, "secret-token") || !strings.Contains(result.Content, "序号越界") || !strings.Contains(result.Content, "达到本次") {
		t.Fatalf("failure result = %#v %v calls=%d", result, err, resolver.calls)
	}
	other := platform.WithMessageContext(ctx, platform.MessageContext{Platform: "p", ScopeID: "other", MediaResolver: resolver})
	result, err = tools.Call(other, tool.CallRequest{Arguments: json.RawMessage(`{"message_id":["1"]}`)})
	if err != nil || resolver.calls != 5 || !strings.Contains(result.Content, "没有该消息") {
		t.Fatalf("scope leaked: %#v %v", result, err)
	}
}

func TestGetMediaSchemaAndHistoryIDTruncation(t *testing.T) {
	schema := (GetMediaTool{}).Schema()
	properties := schema.Function.Parameters["properties"].(map[string]any)
	outer := properties["media_index"].(map[string]any)
	inner := outer["items"].(map[string]any)
	item := inner["items"].(map[string]any)
	if outer["type"] != "array" || inner["type"] != "array" || item["type"] != "integer" {
		t.Fatalf("media index schema = %#v", outer)
	}
	id := "media:" + strings.Repeat("a", 64)
	label := "  1. [图片 " + id + "]"
	result := chatHistoryResultLines([]string{strings.Repeat("文", chatHistoryTextLimit-20), label})
	if strings.Contains(result, "media:") || !strings.Contains(result, "已截断") {
		t.Fatalf("partial ID escaped truncation: %s", result)
	}
}

func TestHistoryQueriesShowMediaWithoutDownload(t *testing.T) {
	ctx, tools, resolver := newHistoryMediaToolTest(t)
	search := NewSearchChatHistoryTool(tools.history)
	search.center = tools.center
	around := NewGetChatHistoryAroundTool(tools.history)
	around.center = tools.center
	for _, call := range []struct {
		tool tool.Tool
		args string
	}{{search, `{}`}, {around, `{"message_id":"1"}`}} {
		result, err := call.tool.Call(ctx, tool.CallRequest{Arguments: json.RawMessage(call.args)})
		if err != nil || resolver.calls != 0 || !strings.Contains(result.Content, "1. [图片 media:未下载]") || len(result.Segments) != 0 {
			t.Fatalf("history result = %#v %v", result, err)
		}
	}
	if _, err := tools.Call(ctx, tool.CallRequest{Arguments: json.RawMessage(`{"message_id":["1"]}`)}); err != nil {
		t.Fatal(err)
	}
	row, err := tools.history.GetByPlatformMessage(ctx, "p", "s", "1")
	if err != nil {
		t.Fatal(err)
	}
	ids, err := tools.center.HistoryIDs(ctx, *row)
	if err != nil {
		t.Fatal(err)
	}
	result, err := search.Call(ctx, tool.CallRequest{Arguments: json.RawMessage(`{}`)})
	if err != nil || resolver.calls != 1 || !strings.Contains(result.Content, "[图片 "+ids[1]+"]") {
		t.Fatalf("downloaded history = %#v %v", result, err)
	}
}
