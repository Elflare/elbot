package tool

import toolshare "elbot/internal/tool/share"

import (
	"context"
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

func TestHistoryQueryErrorLogPreservesBusinessError(t *testing.T) {
	ctx, _, _ := newHistoryMediaToolTest(t)
	callErr := errors.New(strings.Repeat("雪🔥", 700))
	repository := failingHistoryLogRepository{err: callErr}
	for _, value := range []toolshare.Tool{builtin.NewSearchChatHistoryTool(newHistoryTestManager(repository), nil), builtin.NewGetChatHistoryAroundTool(newHistoryTestManager(repository), nil)} {
		result, logContent, err := callTool(ctx, value, toolshare.CallRequest{Arguments: json.RawMessage(`{"message_id":"1"}`)})
		if result != nil || err != callErr || logContent != callErr.Error() || result.LogContent(err) != toolshare.TruncateLogContent(callErr.Error(), 1024) || utf8.RuneCountInString(result.LogContent(err)) != 1024 {
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
		{"search", builtin.NewSearchChatHistoryTool(mediaTool.manager, nil), `{}`, "returned=4"},
		{"around", builtin.NewGetChatHistoryAroundTool(mediaTool.manager, nil), `{"message_id":"long"}`, "message_id=long returned=4"},
		{"empty", builtin.NewSearchChatHistoryTool(mediaTool.manager, nil), `{"query":"no-match"}`, "returned=0"},
		{"empty_around", builtin.NewGetChatHistoryAroundTool(mediaTool.manager, nil), `{"message_id":"missing"}`, "message_id=missing returned=0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, logContent, err := callTool(ctx, tc.value, toolshare.CallRequest{Arguments: json.RawMessage(tc.args)})
			if err != nil {
				t.Fatal(err)
			}
			preview := result.LogContent(err)
			if !strings.HasPrefix(logContent, tc.summary) || preview != toolshare.TruncateLogContent(result.Content, 1024) {
				t.Fatalf("log=%q result=%q", logContent, result.Content)
			}
			if !strings.HasPrefix(tc.name, "empty") && (utf8.RuneCountInString(result.Content) <= 1024 || utf8.RuneCountInString(preview) != 1024 || !utf8.ValidString(preview)) {
				t.Fatalf("result/log budget: %q / %q", result.Content, logContent)
			}
		})
	}
}

func TestGetMediaLogIncludesAllResults(t *testing.T) {
	for _, args := range []string{`{"message_id":["1"]}`, `{"message_id":["missing"]}`, `{"message_id":["1","missing"]}`, `{"message_id":["1"],"media_index":[[1,2,3,4,5,6,7]]}`} {
		t.Run(args, func(t *testing.T) {
			ctx, value, _ := newHistoryMediaToolTest(t)
			result, logContent, err := callTool(ctx, value, toolshare.CallRequest{Arguments: json.RawMessage(args)})
			if err != nil {
				t.Fatal(err)
			}
			if logContent != toolshare.TruncateLogContent(result.Content, 1024) {
				t.Fatalf("log=%q result=%q", logContent, result.Content)
			}
		})
	}
}
