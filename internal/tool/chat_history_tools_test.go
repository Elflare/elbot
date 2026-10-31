package tool

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"elbot/internal/storage"
	"elbot/internal/tool/builtin"
)

type historyResultRepository struct {
	storage.ChatHistoryRepository
	rows []storage.ChatMessage
}

func (r historyResultRepository) Search(context.Context, storage.ChatHistorySearchRequest) ([]storage.ChatMessage, error) {
	return r.rows, nil
}

func (r historyResultRepository) Around(context.Context, storage.ChatHistoryAroundRequest) ([]storage.ChatMessage, error) {
	return r.rows, nil
}

func (r historyResultRepository) GetByPlatformMessage(context.Context, string, string, string) (*storage.ChatMessage, error) {
	return &r.rows[0], nil
}

func TestChatHistoryFullParameter(t *testing.T) {
	ctx, _, _ := newHistoryMediaToolTest(t)
	for _, reference := range []bool{false, true} {
		body := strings.Repeat("雪🔥", 200) + "MESSAGE_TAIL"
		row := storage.ChatMessage{Platform: "p", PlatformMessageID: "42", SenderID: "u", Text: body}
		if reference {
			row.ReplyToPlatformMessageID = "41"
			metadata, _ := json.Marshal(map[string]any{"reply": map[string]string{"text": strings.Repeat("引", 350) + "REFERENCE_TAIL", "sender_id": "v"}})
			row.Metadata = string(metadata)
		}
		repo := historyResultRepository{rows: []storage.ChatMessage{row}}
		for _, caller := range []toolshare.Tool{builtin.NewSearchChatHistoryTool(newHistoryTestManager(repo), nil), builtin.NewGetChatHistoryAroundTool(newHistoryTestManager(repo), nil)} {
			for _, full := range []string{"", `,"full":false`, `,"full":true`} {
				t.Run(caller.Name()+"/"+full+"/reference="+row.ReplyToPlatformMessageID, func(t *testing.T) {
					result, logContent, err := callTool(ctx, caller, toolshare.CallRequest{Arguments: json.RawMessage(`{"message_id":"42"` + full + `}`)})
					if err != nil {
						t.Fatal(err)
					}
					if full == `,"full":true` {
						if !strings.Contains(result.Content, body) || strings.Contains(result.Content, "已截断") || reference && !strings.Contains(result.Content, "REFERENCE_TAIL") {
							t.Fatalf("incomplete full result: %q", result.Content)
						}
					} else if !strings.Contains(result.Content, "...[单条消息过长，已截断]") || strings.Contains(result.Content, "MESSAGE_TAIL") {
						t.Fatalf("missing default truncation: %q", result.Content)
					}
					preview := result.LogContent(err)
					if !strings.Contains(logContent, "returned=1") || preview != toolshare.TruncateLogContent(result.Content, 1024) || utf8.RuneCountInString(preview) > 1024 || !utf8.ValidString(preview) {
						t.Fatalf("invalid log preview: %q", logContent)
					}
				})
			}
		}
	}
}

func TestChatHistoryFullStillBoundsTotalResult(t *testing.T) {
	ctx, _, _ := newHistoryMediaToolTest(t)
	for _, rows := range [][]storage.ChatMessage{
		{{PlatformMessageID: "42", Text: strings.Repeat("文", 6001)}},
		{{PlatformMessageID: "42", Text: strings.Repeat("雪🔥", 2000)}, {PlatformMessageID: "43", Text: strings.Repeat("后", 3000)}},
	} {
		repo := historyResultRepository{rows: rows}
		for _, caller := range []toolshare.Tool{builtin.NewSearchChatHistoryTool(newHistoryTestManager(repo), nil), builtin.NewGetChatHistoryAroundTool(newHistoryTestManager(repo), nil)} {
			result, logContent, err := callTool(ctx, caller, toolshare.CallRequest{Arguments: json.RawMessage(`{"message_id":"42","full":true}`)})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasSuffix(result.Content, "\n...[查询结果过长，已截断]") || strings.Contains(result.Content, "单条消息过长") || strings.Contains(result.Content, rows[len(rows)-1].Text) {
				t.Fatalf("total limit lost: %q", result.Content)
			}
			if len(rows) > 1 && !strings.Contains(result.Content, rows[0].Text) {
				t.Fatal("complete first message was lost")
			}
			preview := result.LogContent(err)
			if !strings.Contains(logContent, "returned=") || preview != toolshare.TruncateLogContent(result.Content, 1024) || utf8.RuneCountInString(preview) > 1024 {
				t.Fatalf("invalid log preview: %q", logContent)
			}
		}
	}
}

func TestChatHistoryPreservesLiteralTruncationText(t *testing.T) {
	ctx, _, _ := newHistoryMediaToolTest(t)
	const body = "用户原文：...[单条消息过长，已截断]"
	repo := historyResultRepository{rows: []storage.ChatMessage{{PlatformMessageID: "42", Text: body}}}
	for _, caller := range []toolshare.Tool{builtin.NewSearchChatHistoryTool(newHistoryTestManager(repo), nil), builtin.NewGetChatHistoryAroundTool(newHistoryTestManager(repo), nil), builtin.NewReplyToChatHistoryMessageTool(newHistoryTestManager(repo))} {
		result, logContent, err := callTool(ctx, caller, toolshare.CallRequest{Arguments: json.RawMessage(`{"message_id":"42","message":"回复"}`)})
		if err != nil || !strings.Contains(result.Content, body) || !strings.Contains(logContent, body) {
			t.Fatalf("%s changed original message: result=%+v log=%q err=%v", caller.Name(), result, logContent, err)
		}
	}
}

func TestChatHistoryReplySeparatesLogPreview(t *testing.T) {
	ctx, _, _ := newHistoryMediaToolTest(t)
	repo := historyResultRepository{rows: []storage.ChatMessage{{PlatformMessageID: "42", Text: strings.Repeat("雪🔥", 200)}}}
	caller := builtin.NewReplyToChatHistoryMessageTool(newHistoryTestManager(repo))
	result, logContent, err := callTool(ctx, caller, toolshare.CallRequest{Arguments: json.RawMessage(`{"message_id":"42","message":"回复"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "...[单条消息过长，已截断]") || result.LogContent(err) != toolshare.TruncateLogContent(result.Content, 1024) || !strings.HasPrefix(logContent, "已引用回复") || len(result.Outputs) != 1 {
		t.Fatalf("result=%+v log=%q", result, logContent)
	}
}

func TestChatHistoryFullSchema(t *testing.T) {
	for _, caller := range []toolshare.Tool{builtin.NewSearchChatHistoryTool(nil, nil), builtin.NewGetChatHistoryAroundTool(nil, nil)} {
		schema := caller.Schema()
		properties := schema.Parameters["properties"].(map[string]any)
		full := properties["full"].(map[string]any)
		required, _ := schema.Parameters["required"].([]string)
		if full["type"] != "boolean" || slices.Contains(required, "full") {
			t.Fatalf("%s full schema: %#v", caller.Name(), schema)
		}
	}
}
