package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/platform"
	sandboxctx "elbot/internal/sandbox"
	"elbot/internal/storage"
	"elbot/internal/tool"
)

func viewImagePNG(t *testing.T) []byte {
	t.Helper()
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewNRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}
func imageCount(result *tool.Result) int {
	if result == nil {
		return 0
	}
	n := 0
	for _, s := range result.Segments {
		if s.Type == llm.SegmentImage {
			n++
		}
	}
	return n
}

func TestViewImageMessageSelectionAndPrevalidation(t *testing.T) {
	ctx, get, resolver := newHistoryMediaToolTest(t)
	resolver.data = viewImagePNG(t)
	row := storage.ChatMessage{Platform: "p", PlatformScopeID: "s", PlatformMessageID: "mixed", Segments: platform.MarshalChatSegments([]platform.MessageSegment{
		{Type: platform.SegmentFile, PlatformFileID: "file"}, {Type: platform.SegmentImage, PlatformFileID: "a"}, {Type: platform.SegmentImage, PlatformFileID: "b"},
	})}
	if err := get.history.Append(ctx, &row); err != nil {
		t.Fatal(err)
	}
	viewer := NewViewImageTool(get.center, get.history)
	call := func(raw string) (*tool.Result, error) {
		return viewer.Call(ctx, tool.CallRequest{Arguments: json.RawMessage(raw)})
	}
	for _, raw := range []string{`{}`, `{"message_id":[]}`, `{"message_id":["mixed"],"media_index":[[]]}`, `{"message_id":["mixed"],"media_index":[[0]]}`, `{"message_id":["mixed"],"media_index":[[1]]}`, `{"message_id":["mixed"],"media_index":[[4]]}`, `{"message_id":["mixed"],"media_index":[]}`, `{"source":"media:x","message_id":["mixed"]}`} {
		if _, err := call(raw); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if _, err := call(`{"message_id":["mixed","missing"]}`); err == nil || resolver.calls != 0 {
		t.Fatalf("prevalidation downloaded: calls=%d err=%v", resolver.calls, err)
	}
	result, err := call(`{"message_id":["#mixed"]}`)
	if err != nil || imageCount(result) != 1 || resolver.calls != 1 || !strings.Contains(llm.SegmentsTextOnly(result.Segments), "2.") || len(result.Outputs) != 0 {
		t.Fatalf("default=%+v err=%v", result, err)
	}
	result, err = call(`{"message_id":["mixed"],"media_index":[[3,2]]}`)
	if err != nil || imageCount(result) != 2 || resolver.calls != 2 || !strings.HasPrefix(llm.SegmentsTextOnly(result.Segments), "[#mixed] 3.") {
		t.Fatalf("selection=%+v err=%v calls=%d", result, err, resolver.calls)
	}
	other := platform.WithMessageContext(ctx, platform.MessageContext{Conversation: contextinfo.Conversation{Source: contextinfo.Source{Platform: "p", ScopeID: "other"}}})
	if _, err := viewer.Call(other, tool.CallRequest{Arguments: json.RawMessage(`{"message_id":["mixed"]}`)}); err == nil {
		t.Fatal("cross-chat lookup allowed")
	}
	result, err = call(`{"message_id":["1"],"media_index":[[1,2,3,4,5,6]]}`)
	if err != nil || imageCount(result) != 5 || !strings.Contains(llm.SegmentsTextOnly(result.Segments), "5 个下载") {
		t.Fatalf("budget=%+v err=%v", result, err)
	}
}

func TestViewImageSourcesAndPermissions(t *testing.T) {
	ctx, get, _ := newHistoryMediaToolTest(t)
	viewer := NewViewImageTool(get.center, get.history)
	data := viewImagePNG(t)
	item, err := get.center.ImportBytes(ctx, data, media.Input{Name: "image.png"})
	if err != nil {
		t.Fatal(err)
	}
	call := func(ctx context.Context, source string) (*tool.Result, error) {
		raw, _ := json.Marshal(viewImageArgs{Source: source})
		return viewer.Call(ctx, tool.CallRequest{Arguments: raw})
	}
	if result, err := call(ctx, item.ID); err != nil || imageCount(result) != 1 {
		t.Fatalf("media=%+v %v", result, err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(data)
	}))
	defer server.Close()
	if result, err := call(ctx, server.URL); err != nil || imageCount(result) != 1 {
		t.Fatalf("url=%+v %v", result, err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "image.png")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := call(ctx, path); err == nil || !strings.Contains(err.Error(), "超级管理员") {
		t.Fatalf("local permission=%v", err)
	}
	admin := contextinfo.WithActor(ctx, contextinfo.Actor{Role: contextinfo.RoleSuperadmin})
	for _, source := range []string{path, "file://" + path} {
		if result, err := call(admin, source); err != nil || imageCount(result) != 1 {
			t.Fatalf("local=%+v %v", result, err)
		}
	}
	background := sandboxctx.WithSandboxContext(admin, sandboxctx.SandboxContext{Background: true, Dir: dir, Root: dir})
	if result, err := call(background, "image.png"); err != nil || imageCount(result) != 1 {
		t.Fatalf("sandbox=%+v %v", result, err)
	}
	for _, source := range []string{path, "../image.png"} {
		if _, err := call(background, source); err == nil {
			t.Fatalf("escaped sandbox: %s", source)
		}
	}
	bad, err := get.center.ImportBytes(ctx, []byte("not an image"), media.Input{Name: "broken.png", MIMEType: "image/png"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := call(ctx, bad.ID); err == nil {
		t.Fatal("accepted broken image")
	}
	truncated, err := get.center.ImportBytes(ctx, data[:33], media.Input{Name: "truncated.png"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := call(ctx, truncated.ID); err == nil {
		t.Fatal("accepted image with only a valid header")
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), data, 0600); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(viewImageArgs{Source: filepath.Join(dir, ".env")})
	assessment, err := viewer.AssessRisk(admin, tool.CallRequest{Arguments: raw})
	if err != nil || assessment.Level != tool.RiskHigh {
		t.Fatalf("sensitive image source risk=%+v err=%v", assessment, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := call(canceled, server.URL); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestViewImagePartialFailuresKeepCachedImages(t *testing.T) {
	ctx, get, resolver := newHistoryMediaToolTest(t)
	resolver.data = viewImagePNG(t)
	viewer := NewViewImageTool(get.center, get.history)
	if _, err := viewer.Call(ctx, tool.CallRequest{Arguments: json.RawMessage(`{"message_id":["1"]}`)}); err != nil {
		t.Fatal(err)
	}
	resolver.fail = true
	result, err := viewer.Call(ctx, tool.CallRequest{Arguments: json.RawMessage(`{"message_id":["1"],"media_index":[[2,1,2]]}`)})
	if err != nil || imageCount(result) != 1 || resolver.calls != 2 {
		t.Fatalf("partial result=%+v err=%v calls=%d", result, err, resolver.calls)
	}
	text := llm.SegmentsTextOnly(result.Segments)
	if strings.Count(text, "图片获取失败") != 2 || strings.Contains(text, "secret-token") || len(result.Outputs) != 0 {
		t.Fatalf("partial failure labels=%s", text)
	}
}

func TestViewImageHistoryHintUsesAPIType(t *testing.T) {
	ctx, get, _ := newHistoryMediaToolTest(t)
	for _, apiType := range []string{"", "chat", "response"} {
		ctx := contextinfo.WithModel(ctx, contextinfo.Model{APIType: apiType})
		hint := viewImageHistoryHint(ctx, get.center)
		if (hint != "") != (apiType == "response") {
			t.Fatalf("%s hint=%q", apiType, hint)
		}
	}
}
