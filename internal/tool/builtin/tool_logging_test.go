package builtin

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"elbot/internal/fileops"
	"elbot/internal/storage"
	
)

type failingHistoryLogRepository struct {
	storage.ChatHistoryRepository
	err error
}

func (r failingHistoryLogRepository) Search(context.Context, storage.ChatHistorySearchRequest) ([]storage.ChatMessage, error) {
	return nil, r.err
}

func (r failingHistoryLogRepository) Around(context.Context, storage.ChatHistoryAroundRequest) ([]storage.ChatMessage, error) {
	return nil, r.err
}

func TestHistoryQueryErrorLogPreservesBusinessError(t *testing.T) {
	ctx, _, _ := newHistoryMediaToolTest(t)
	callErr := errors.New(strings.Repeat("雪🔥", 700))
	repository := failingHistoryLogRepository{err: callErr}
	for _, value := range []toolshare.Tool{NewSearchChatHistoryTool(repository), NewGetChatHistoryAroundTool(repository)} {
		result, logContent, err := value.Call(ctx, toolshare.CallRequest{Arguments: json.RawMessage(`{"message_id":"1"}`)})
		if result != nil || err != callErr || logContent != toolshare.TruncateLogContent(callErr.Error(), 1024) || utf8.RuneCountInString(logContent) != 1024 {
			t.Fatalf("%s: result=%+v log=%q err=%v", value.Name(), result, logContent, err)
		}
	}
}

func TestHistoryQueryLogIncludesResult(t *testing.T) {
	ctx, mediaTool, _ := newHistoryMediaToolTest(t)
	for _, messageID := range []string{"long", "longer"} {
		if err := mediaTool.history.Append(ctx, &storage.ChatMessage{Platform: "p", PlatformScopeID: "s", PlatformMessageID: messageID, SenderID: "u", Text: strings.Repeat("雪🔥", 700)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name          string
		value         toolshare.Tool
		args, summary string
	}{
		{"search", NewSearchChatHistoryTool(mediaTool.history), `{}`, "returned=4"},
		{"around", NewGetChatHistoryAroundTool(mediaTool.history), `{"message_id":"long"}`, "message_id=long returned=4"},
		{"empty", NewSearchChatHistoryTool(mediaTool.history), `{"query":"no-match"}`, "returned=0"},
		{"empty_around", NewGetChatHistoryAroundTool(mediaTool.history), `{"message_id":"missing"}`, "message_id=missing returned=0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, logContent, err := tc.value.Call(ctx, toolshare.CallRequest{Arguments: json.RawMessage(tc.args)})
			if err != nil {
				t.Fatal(err)
			}
			summary, body, ok := strings.Cut(logContent, "\ncontent:\n")
			want := strings.ReplaceAll(result.Content, "...[单条消息过长，已截断]", "…")
			if !ok || summary != tc.summary || logContent != toolshare.TruncateLogContent(tc.summary+"\ncontent:\n"+want, 1024) {
				t.Fatalf("log=%q result=%q", logContent, result.Content)
			}
			if !strings.HasPrefix(tc.name, "empty") && (utf8.RuneCountInString(result.Content) <= 1024 || utf8.RuneCountInString(logContent) != 1024 || !utf8.ValidString(body)) {
				t.Fatalf("result/log budget: %q / %q", result.Content, body)
			}
		})
	}
}

func TestGetMediaLogIncludesAllResults(t *testing.T) {
	for _, args := range []string{`{"message_id":["1"]}`, `{"message_id":["missing"]}`, `{"message_id":["1","missing"]}`, `{"message_id":["1"],"media_index":[[1,2,3,4,5,6,7]]}`} {
		t.Run(args, func(t *testing.T) {
			ctx, value, _ := newHistoryMediaToolTest(t)
			result, logContent, err := value.Call(ctx, toolshare.CallRequest{Arguments: json.RawMessage(args)})
			if err != nil {
				t.Fatal(err)
			}
			if logContent != toolshare.TruncateLogContent(result.Content, 1024) {
				t.Fatalf("log=%q result=%q", logContent, result.Content)
			}
		})
	}
}

func TestFileLogs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.txt")
	body := strings.Repeat("雪🔥", 700) + "BUSINESS_TAIL"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"path": path})
	result, logContent, err := NewReadFileTool().Call(t.Context(), toolshare.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, body) || strings.Contains(logContent, "BUSINESS_TAIL") || !strings.Contains(logContent, "lines=1-1/1") || !strings.Contains(logContent, strings.Repeat("雪🔥", 200)) || !strings.HasSuffix(logContent, "…") || !utf8.ValidString(logContent) || utf8.RuneCountInString(logContent) != 1024 {
		t.Fatalf("read log=%q business bytes=%d", logContent, len(result.Content))
	}
	args, _ = json.Marshal(map[string]any{"path": path, "expected_revision": fileops.ContentRevision([]byte(body)), "edits": []map[string]any{{"operation": "overwrite", "new_text": body + "EDIT_TAIL"}}})
	result, logContent, err = NewEditFileTool().Call(t.Context(), toolshare.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(path)
	if err != nil || string(written) != body+"EDIT_TAIL" || !strings.Contains(result.Content, "EDIT_TAIL") {
		t.Fatalf("business edit changed: err=%v result=%+v", err, result)
	}
	if !strings.Contains(logContent, "diff:") || strings.Contains(logContent, "revision_before") || !strings.HasSuffix(logContent, "…") || !utf8.ValidString(logContent) || utf8.RuneCountInString(logContent) != 1024 {
		t.Fatalf("edit log=%q", logContent)
	}
}
