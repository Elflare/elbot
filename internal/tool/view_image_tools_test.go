package tool

import toolshare "elbot/internal/tool/share"

import (
	"bytes"
	"context"
	"elbot/internal/operationresult"
	"elbot/internal/testutil"
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
	"elbot/internal/storage"
	"elbot/internal/tool/builtin"
	"elbot/internal/workspace"
)

func viewImagePNG(t *testing.T) []byte {
	t.Helper()
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewNRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}
func imageCount(result *toolshare.Result) int {
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
	row := storage.ChatMessage{Platform: "p", ChatID: "s", PlatformMessageID: "mixed", Segments: storage.MarshalChatSegments([]storage.ChatSegment{
		{Type: "file", PlatformFileID: "file"}, {Type: "image", PlatformFileID: "a"}, {Type: "image", PlatformFileID: "b"},
	})}
	if err := get.history.Append(ctx, &row); err != nil {
		t.Fatal(err)
	}
	viewer := builtinTestTool(t, "view_image", builtin.Deps{Media: get.center, ChatHistory: get.manager})
	call := func(raw string) (operationresult.Result[*toolshare.Result], error) {
		result, _, err := callTool(ctx, viewer, toolshare.CallRequest{Arguments: json.RawMessage(raw)})
		return result, err
	}
	for _, raw := range []string{`{}`, `{"message_id":[]}`, `{"message_id":["mixed"],"media_index":[[]]}`, `{"message_id":["mixed"],"media_index":[[0]]}`, `{"message_id":["mixed"],"media_index":[[1]]}`, `{"message_id":["mixed"],"media_index":[[4]]}`, `{"message_id":["mixed"],"media_index":[]}`, `{"source":"media:x","message_id":["mixed"]}`} {
		if result, err := call(raw); err != nil || result.Status == operationresult.Success {
			t.Fatalf("accepted %s", raw)
		}
	}
	if result, err := call(`{"message_id":["mixed","missing"]}`); err != nil || result.Status == operationresult.Success || resolver.calls != 0 {
		t.Fatalf("prevalidation downloaded: calls=%d err=%v", resolver.calls, err)
	}
	result, err := call(`{"message_id":["#mixed"]}`)
	if err != nil || result.Status != operationresult.Success || imageCount(result.Data) != 1 || resolver.calls != 1 || !strings.Contains(llm.SegmentsTextOnly(result.Data.Segments), "2.") || len(result.Data.Outputs) != 0 {
		t.Fatalf("default=%+v err=%v", result, err)
	}
	result, err = call(`{"message_id":["mixed"],"media_index":[[3,2]]}`)
	if err != nil || result.Status != operationresult.Success || imageCount(result.Data) != 2 || resolver.calls != 2 || !strings.HasPrefix(llm.SegmentsTextOnly(result.Data.Segments), "[#mixed] 3.") {
		t.Fatalf("selection=%+v err=%v calls=%d", result, err, resolver.calls)
	}
	other := contextinfo.WithConversation(ctx, contextinfo.Conversation{Source: contextinfo.Source{Platform: "p", ChatID: "other"}})
	if result, _, err := callTool(other, viewer, toolshare.CallRequest{Arguments: json.RawMessage(`{"message_id":["mixed"]}`)}); err != nil || result.Status == operationresult.Success {
		t.Fatal("cross-chat lookup allowed")
	}
	result, err = call(`{"message_id":["1"],"media_index":[[1,2,3,4,5,6]]}`)
	if err != nil || result.Status != operationresult.Success || imageCount(result.Data) != 5 || !strings.Contains(llm.SegmentsTextOnly(result.Data.Segments), "5 个下载") {
		t.Fatalf("budget=%+v err=%v", result, err)
	}
}

func TestViewImageSourcesAndPermissions(t *testing.T) {
	ctx, get, _ := newHistoryMediaToolTest(t)
	viewer := builtinTestTool(t, "view_image", builtin.Deps{Media: get.center, ChatHistory: get.manager})
	data := viewImagePNG(t)
	item, err := get.mediaCenter.ImportBytes(ctx, data, media.Input{Name: "image.png"}, media.ImportLimits{})
	if err != nil {
		t.Fatal(err)
	}
	call := func(ctx context.Context, source string) (operationresult.Result[*toolshare.Result], error) {
		raw, _ := json.Marshal(map[string]string{"source": source})
		result, _, err := callTool(ctx, viewer, toolshare.CallRequest{Arguments: raw})
		return result, err
	}
	if result, err := call(ctx, item.Data.ID); err != nil || result.Status != operationresult.Success || imageCount(result.Data) != 1 {
		t.Fatalf("media=%+v %v", result, err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(data)
	}))
	defer server.Close()
	if result, err := call(ctx, server.URL); err != nil || result.Status != operationresult.Success || imageCount(result.Data) != 1 {
		t.Fatalf("url=%+v %v", result, err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "image.png")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if result, err := call(ctx, path); err != nil || result.Status == operationresult.Success || !strings.Contains(result.Message, "超级管理员") {
		t.Fatalf("local permission=%v", err)
	}
	admin := contextinfo.WithActor(ctx, contextinfo.Actor{Role: contextinfo.RoleSuperadmin})
	for _, source := range []string{path, "file://" + path} {
		if result, err := call(admin, source); err != nil || result.Status != operationresult.Success || imageCount(result.Data) != 1 {
			t.Fatalf("local=%+v %v", result, err)
		}
	}
	background := workspace.WithPathScope(testutil.TaskContext(admin, ""), workspace.PathScope{Dir: dir, Restricted: true})
	if result, err := call(background, "image.png"); err != nil || result.Status != operationresult.Success || imageCount(result.Data) != 1 {
		t.Fatalf("sandbox=%+v %v", result, err)
	}
	for _, source := range []string{path, "../image.png"} {
		if result, err := call(background, source); err != nil || result.Status == operationresult.Success {
			t.Fatalf("escaped sandbox: %s", source)
		}
	}
	bad, err := get.mediaCenter.ImportBytes(ctx, []byte("not an image"), media.Input{Name: "broken.png", MIMEType: "image/png"}, media.ImportLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := call(ctx, bad.Data.ID); err != nil || result.Status == operationresult.Success {
		t.Fatal("accepted broken image")
	}
	truncated, err := get.mediaCenter.ImportBytes(ctx, data[:33], media.Input{Name: "truncated.png"}, media.ImportLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := call(ctx, truncated.Data.ID); err != nil || result.Status == operationresult.Success {
		t.Fatal("accepted image with only a valid header")
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), data, 0600); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]string{"source": filepath.Join(dir, ".env")})
	assessment, err := preparedRisk(admin, viewer, toolshare.CallRequest{Arguments: raw})
	if err != nil || assessment.Status != operationresult.Success || assessment.Data.Level != toolshare.RiskHigh {
		t.Fatalf("sensitive image source risk=%+v err=%v", assessment, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if result, err := call(canceled, server.URL); err != nil || result.Status == operationresult.Success {
		t.Fatal("ignored cancellation")
	}
}

func TestViewImagePartialFailuresKeepCachedImages(t *testing.T) {
	ctx, get, resolver := newHistoryMediaToolTest(t)
	resolver.data = viewImagePNG(t)
	viewer := builtinTestTool(t, "view_image", builtin.Deps{Media: get.center, ChatHistory: get.manager})
	if result, _, err := callTool(ctx, viewer, toolshare.CallRequest{Arguments: json.RawMessage(`{"message_id":["1"]}`)}); err != nil || result.Status != operationresult.Success {
		t.Fatal(err)
	}
	resolver.fail = true
	result, _, err := callTool(ctx, viewer, toolshare.CallRequest{Arguments: json.RawMessage(`{"message_id":["1"],"media_index":[[2,1,2]]}`)})
	if err != nil || result.Status != operationresult.Success || imageCount(result.Data) != 1 || resolver.calls != 2 {
		t.Fatalf("partial result=%+v err=%v calls=%d", result, err, resolver.calls)
	}
	text := llm.SegmentsTextOnly(result.Data.Segments)
	if strings.Count(text, "https://secret-token/private") != 2 || len(result.Data.Outputs) != 0 {
		t.Fatalf("partial failure labels=%s", text)
	}
}
