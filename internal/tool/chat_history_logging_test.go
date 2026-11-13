package tool

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"elbot/internal/operationresult"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"elbot/internal/storage"
	"elbot/internal/tool/builtin"
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

func TestHistoryQueryFaultPreservesOriginalError(t *testing.T) {
	ctx, _, _ := newHistoryMediaToolTest(t)
	callErr := errors.New(strings.Repeat("雪🔥", 700))
	repository := failingHistoryLogRepository{err: callErr}
	for _, value := range []toolshare.ToolDefinition{builtinTestTool(t, "search_chat_history", builtin.Deps{ChatHistory: newHistoryTestManager(repository)}), builtinTestTool(t, "get_chat_history_around", builtin.Deps{ChatHistory: newHistoryTestManager(repository)})} {
		result, logContent, err := callTool(ctx, value, toolshare.CallRequest{Arguments: json.RawMessage(`{"message_id":"1"}`)})
		if result.Data != nil || err != callErr || logContent != callErr.Error() {
			t.Fatalf("%s: result=%+v log=%q err=%v", value.Definition.Name, result, logContent, err)
		}
	}
}

func TestHistoryQueryLogIncludesResult(t *testing.T) {
	ctx, mediaTool, _ := newHistoryMediaToolTest(t)
	for _, messageID := range []string{"long", "longer"} {
		if err := mediaTool.history.Append(ctx, &storage.ChatMessage{Platform: "p", ChatID: "s", PlatformMessageID: messageID, SenderID: "u", Text: strings.Repeat("雪🔥", 700)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name          string
		value         toolshare.ToolDefinition
		args, summary string
	}{
		{"search", builtinTestTool(t, "search_chat_history", builtin.Deps{ChatHistory: mediaTool.manager}), `{}`, "returned=4"},
		{"around", builtinTestTool(t, "get_chat_history_around", builtin.Deps{ChatHistory: mediaTool.manager}), `{"message_id":"long"}`, "returned=4"},
		{"empty", builtinTestTool(t, "search_chat_history", builtin.Deps{ChatHistory: mediaTool.manager}), `{"query":"no-match"}`, "returned=0"},
		{"empty_around", builtinTestTool(t, "get_chat_history_around", builtin.Deps{ChatHistory: mediaTool.manager}), `{"message_id":"missing"}`, "message_id=missing returned=0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, logContent, err := callTool(ctx, tc.value, toolshare.CallRequest{Arguments: json.RawMessage(tc.args)})
			if err != nil || result.Status != operationresult.Success {
				t.Fatal(err)
			}
			preview := result.Data.LogContent()
			if !strings.Contains(logContent, tc.summary) || preview != toolshare.TruncateLogContent(result.Data.Content, 1024) {
				t.Fatalf("log=%q result=%q", logContent, result.Data.Content)
			}
			if !strings.HasPrefix(tc.name, "empty") && (utf8.RuneCountInString(result.Data.Content) <= 1024 || utf8.RuneCountInString(preview) != 1024 || !utf8.ValidString(preview)) {
				t.Fatalf("result/log budget: %q / %q", result.Data.Content, logContent)
			}
		})
	}
}

func TestGetMediaLogSummarizesResults(t *testing.T) {
	for _, args := range []string{`{"message_id":["1"]}`, `{"message_id":["missing"]}`, `{"message_id":["1","missing"]}`, `{"message_id":["1"],"media_index":[[1,2,3,4,5,6,7]]}`} {
		t.Run(args, func(t *testing.T) {
			ctx, value, _ := newHistoryMediaToolTest(t)
			result, logContent, err := callTool(ctx, (value).ToolDefinition, toolshare.CallRequest{Arguments: json.RawMessage(args)})
			if err != nil || result.Status != operationresult.Success {
				t.Fatal(err)
			}
			if !strings.HasPrefix(logContent, "requested_messages=") || strings.Contains(logContent, "\n") || result.Data.LogContent() != toolshare.TruncateLogContent(result.Data.Content, 1024) {
				t.Fatalf("log=%q result=%q", logContent, result.Data.Content)
			}
		})
	}
}
