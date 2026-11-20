package tool

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/operationresult"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
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

func (r historyResultRepository) GetBySeq(context.Context, string, contextinfo.ChatType, string, int64, bool) (*storage.ChatMessage, error) {
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
		for _, caller := range []toolshare.ToolDefinition{builtinTestTool(t, "search_chat_history", builtin.Deps{ChatHistory: historyTestDeps(repo)}), builtinTestTool(t, "get_chat_history_around", builtin.Deps{ChatHistory: historyTestDeps(repo)})} {
			for _, full := range []string{"", `,"full":false`, `,"full":true`} {
				t.Run(caller.Definition.Name+"/"+full+"/reference="+row.ReplyToPlatformMessageID, func(t *testing.T) {
					result, logContent, err := callTool(ctx, caller, toolshare.CallRequest{Arguments: json.RawMessage(`{"message_id":"42"` + full + `}`)})
					if err != nil || result.Status != operationresult.Success {
						t.Fatal(err)
					}
					if caller.Definition.Name == "get_chat_history_around" && full == `,"full":true` {
						if !strings.Contains(result.Data.Content, body) || strings.Contains(result.Data.Content, "已截断") || reference && !strings.Contains(result.Data.Content, "REFERENCE_TAIL") {
							t.Fatalf("incomplete full result: %q", result.Data.Content)
						}
					} else if !strings.Contains(result.Data.Content, "...[单条消息过长，已截断]") || strings.Contains(result.Data.Content, "MESSAGE_TAIL") {
						t.Fatalf("missing default truncation: %q", result.Data.Content)
					}
					preview := result.Data.LogContent()
					if !strings.Contains(logContent, "returned=1") || preview != toolshare.TruncateLogContent(result.Data.Content, 1024) || utf8.RuneCountInString(preview) > 1024 || !utf8.ValidString(preview) {
						t.Fatalf("invalid log preview: %q", logContent)
					}
				})
			}
		}
	}
}

func TestHistorySearchHasNoTotalBudget(t *testing.T) {
	ctx, _, _ := newHistoryMediaToolTest(t)
	rows := make([]storage.ChatMessage, 50)
	for i := range rows {
		rows[i] = storage.ChatMessage{Seq: int64(i + 1), SenderID: "u", Text: strings.Repeat("文", 300)}
	}
	repo := historyResultRepository{rows: rows}
	caller := builtinTestTool(t, "search_chat_history", builtin.Deps{ChatHistory: historyTestDeps(repo)})
	res, _, err := callTool(ctx, caller, toolshare.CallRequest{Arguments: json.RawMessage(`{"limit":50}`)})
	if err != nil || res.Status != operationresult.Success || strings.Count(res.Data.Content, rows[0].Text) != 50 || !strings.Contains(res.Data.Content, "h50 ") {
		t.Fatalf("missing rows: %v %v", res, err)
	}
}

func TestChatHistoryPreservesLiteralTruncationText(t *testing.T) {
	ctx, _, _ := newHistoryMediaToolTest(t)
	const body = "用户原文：...[单条消息过长，已截断]"
	repo := historyResultRepository{rows: []storage.ChatMessage{{PlatformMessageID: "42", Text: body}}}
	for _, caller := range []toolshare.ToolDefinition{builtinTestTool(t, "search_chat_history", builtin.Deps{ChatHistory: historyTestDeps(repo)}), builtinTestTool(t, "get_chat_history_around", builtin.Deps{ChatHistory: historyTestDeps(repo)}), builtinTestTool(t, "reply_to_chat_history_message", builtin.Deps{ChatHistory: historyTestDeps(repo)})} {
		result, _, err := callTool(ctx, caller, toolshare.CallRequest{Arguments: json.RawMessage(`{"message_id":"42","message":"回复"}`)})
		if err != nil || result.Status != operationresult.Success || !strings.Contains(result.Data.Content, body) {
			t.Fatalf("%s changed original message: result=%+v err=%v", caller.Definition.Name, result, err)
		}
	}
}

func TestChatHistoryReplySeparatesLogPreview(t *testing.T) {
	ctx, _, _ := newHistoryMediaToolTest(t)
	repo := historyResultRepository{rows: []storage.ChatMessage{{PlatformMessageID: "42", Text: strings.Repeat("雪🔥", 200)}}}
	caller := builtinTestTool(t, "reply_to_chat_history_message", builtin.Deps{ChatHistory: historyTestDeps(repo)})
	result, logContent, err := callTool(ctx, caller, toolshare.CallRequest{Arguments: json.RawMessage(`{"message_id":"42","message":"回复"}`)})
	if err != nil || result.Status != operationresult.Success {
		t.Fatal(err)
	}
	if !strings.Contains(result.Data.Content, "...[单条消息过长，已截断]") || result.Data.LogContent() != toolshare.TruncateLogContent(result.Data.Content, 1024) || !strings.HasPrefix(logContent, "已引用回复") || len(result.Data.Outputs) != 1 {
		t.Fatalf("result=%+v log=%q", result, logContent)
	}
}

func TestFullHistoryIgnoresWindow(t *testing.T) {
	ctx, fixture, _ := newHistoryMediaToolTest(t)
	repo := fixture.history
	full := strings.Repeat("完整正文\n", 1900)
	for i, text := range []string{"不应展示前文", full, "不应展示后文"} {
		row := storage.ChatMessage{Platform: "p", ChatID: "s", PlatformMessageID: fmt.Sprint("native", i), SenderID: "sender", Text: text, SenderAvatarURL: "https://example.test/avatar"}
		if err := repo.Append(ctx, &row); err != nil {
			t.Fatal(err)
		}
	}
	target, err := repo.GetByPlatformMessage(ctx, "p", "", "s", "native1")
	if err != nil {
		t.Fatal(err)
	}
	caller := builtinTestTool(t, "get_chat_history_around", builtin.Deps{ChatHistory: fixture.deps})
	for _, avatar := range []bool{false, true} {
		args := fmt.Sprintf(`{"message_id":"h%d","full":true,"before":50,"after":50,"include_avatar":%t}`, target.Seq, avatar)
		res, _, err := callTool(ctx, caller, toolshare.CallRequest{Arguments: json.RawMessage(args)})
		if err != nil || res.Status != operationresult.Success {
			t.Fatalf("%v %v", res, err)
		}
		body := res.Data.Content
		if !strings.Contains(body, full) || strings.Contains(body, "不应展示") || strings.Contains(body, "https://example.test/avatar") != avatar {
			t.Errorf("wrong full result (avatar=%v)", avatar)
		}
	}
}

func TestChatHistoryIncludeAvatar(t *testing.T) {
	ctx, fixture, _ := newHistoryMediaToolTest(t)
	ctx = contextinfo.WithConversation(ctx, contextinfo.Conversation{Source: contextinfo.Source{Platform: "qqofficial", ChatType: "group", ChatID: "g"}})
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := old.Add(24 * time.Hour)
	add := func(id, chat, user, text, url string, at time.Time) {
		t.Helper()
		err := fixture.history.Append(ctx, &storage.ChatMessage{Platform: "qqofficial", ChatType: "group", ChatID: chat, PlatformMessageID: id, SenderID: user, Text: text, SenderAvatarURL: url, CreatedAt: at})
		if err != nil {
			t.Fatal(err)
		}
	}
	add("a", "g", "alice", "needle1", "https://ex/old", old)
	add("b", "g", "alice", "needle2", "", old)
	add("c", "g", "bob", "needle3", "", old)
	add("d", "g", "alice", "today", "https://ex/tie", now)
	add("e", "g", "alice", "today", "https://ex/latest", now)
	add("f", "g", "alice", "late", "https://ex/late", now.Add(-time.Nanosecond).In(time.FixedZone("east", 8*3600)))
	add("h", "g", "alice", "today", "", now.Add(time.Hour))
	add("i", "other", "alice", "elsewhere", "https://ex/other", now.Add(time.Hour))
	target, err := fixture.history.GetByPlatformMessage(ctx, "qqofficial", "group", "g", "b")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"search_chat_history", "get_chat_history_around"} {
		caller := builtinTestTool(t, name, builtin.Deps{ChatHistory: fixture.deps})
		args := `{"query":"needle"`
		if name == "get_chat_history_around" {
			args = fmt.Sprintf(`{"message_id":"h%d","before":1,"after":1`, target.Seq)
		}
		run := func(option string) string {
			t.Helper()
			res, _, err := callTool(ctx, caller, toolshare.CallRequest{Arguments: json.RawMessage(args + option + `}`)})
			if err != nil || res.Status != operationresult.Success {
				t.Fatalf("%s: %v %v", name, res, err)
			}
			return res.Data.Content
		}
		off := run("")
		if run(`,"include_avatar":false`) != off || strings.Contains(off, "https://") {
			t.Fatalf("unexpected default: %s", off)
		}
		want := strings.Replace(off, "u1 alice", "u1 alice https://ex/latest", 1)
		if got := run(`,"include_avatar":true`); got != want {
			t.Errorf("%s got %q, want %q", name, got, want)
		}
	}
}

func TestHistoryShortIDRoundTrip(t *testing.T) {
	ctx, fixture, _ := newHistoryMediaToolTest(t)
	for _, platform := range []string{"qqonebot", "qqofficial", "telegram"} {
		t.Run(platform, func(t *testing.T) {
			source := contextinfo.Source{Platform: platform, ChatType: "group", ChatID: "short-id-test"}
			ctx := contextinfo.WithConversation(ctx, contextinfo.Conversation{Source: source})
			const nativeID = "876543210"
			row := storage.ChatMessage{Platform: platform, ChatType: source.ChatType, ChatID: source.ChatID, PlatformMessageID: nativeID, SenderID: "sender", Text: "目标正文"}
			if err := fixture.history.Append(ctx, &row); err != nil {
				t.Fatal(err)
			}
			stored, err := fixture.history.GetByPlatformMessage(ctx, platform, source.ChatType, source.ChatID, nativeID)
			if err != nil {
				t.Fatal(err)
			}
			short := fmt.Sprintf("h%d", stored.Seq)
			around := builtinTestTool(t, "get_chat_history_around", builtin.Deps{ChatHistory: fixture.deps})
			for _, id := range []string{"#8121", "ROBOT1.0_old", "h0", "-1", "h9223372036854775808", nativeID} {
				res, _, err := callTool(ctx, around, toolshare.CallRequest{Arguments: json.RawMessage(fmt.Sprintf(`{"message_id":%q}`, id))})
				want := operationresult.InvalidArguments
				if id == nativeID {
					want = operationresult.NotFound
				}
				if err != nil || res.Status != want {
					t.Errorf("%s: got %s, %v; want %s", id, res.Status, err, want)
				}
			}
			reply := builtinTestTool(t, "reply_to_chat_history_message", builtin.Deps{ChatHistory: fixture.deps})
			args := toolshare.CallRequest{Arguments: json.RawMessage(fmt.Sprintf(`{"message_id":%q,"message":"确认"}`, short))}
			if platform == "qqofficial" {
				res, _, err := callTool(ctx, reply, args)
				if err != nil || res.Status != operationresult.InvalidTarget {
					t.Fatalf("missing index: %v %v", res, err)
				}
				if err := fixture.history.MapPlatformMessageAlias(ctx, storage.PlatformMessageAlias{Platform: platform, ChatType: source.ChatType, ChatID: source.ChatID, PlatformMessageID: nativeID, Alias: "REFIDX_target"}); err != nil {
					t.Fatal(err)
				}
			}
			res, _, err := callTool(ctx, reply, args)
			if err != nil || res.Status != operationresult.Success || len(res.Data.Outputs) != 1 {
				t.Fatalf("reply: %v %v", res, err)
			}
			out := res.Data.Outputs[0]
			if out.ReplyToPlatformMessageID != nativeID || (platform == "qqofficial" && out.ReplyReferenceIndex != "REFIDX_target") {
				t.Fatal(out)
			}
			for _, tc := range []struct{ name, args string }{
				{"search_chat_history", `{}`},
				{"get_chat_history_around", fmt.Sprintf(`{"message_id":%q}`, short)},
				{"get_chat_history_around", fmt.Sprintf(`{"message_id":"%d"}`, stored.Seq)},
			} {
				tool := builtinTestTool(t, tc.name, builtin.Deps{ChatHistory: fixture.deps})
				res, _, err := callTool(ctx, tool, toolshare.CallRequest{Arguments: json.RawMessage(tc.args)})
				if err != nil || res.Status != operationresult.Success {
					t.Errorf("%s(%s): %v, %v", tc.name, tc.args, res, err)
					continue
				}
				if body := res.Data.Content; !strings.Contains(body, short) || strings.Contains(body, nativeID) || !strings.Contains(body, row.Text) {
					t.Errorf("%s(%s): %s", tc.name, tc.args, body)
				}
			}
		})
	}
}
func TestChatHistoryFullSchema(t *testing.T) {
	for _, caller := range []toolshare.ToolDefinition{builtinTestTool(t, "search_chat_history", builtin.Deps{}), builtinTestTool(t, "get_chat_history_around", builtin.Deps{})} {
		schema := caller.Schema()
		properties := schema.Parameters["properties"].(map[string]any)
		if caller.Definition.Name == "search_chat_history" {
			if _, ok := properties["full"]; ok {
				t.Fatal("search exposes full")
			}
			continue
		}
		full := properties["full"].(map[string]any)
		required, _ := schema.Parameters["required"].([]string)
		if full["type"] != "boolean" || slices.Contains(required, "full") {
			t.Fatalf("%s full schema: %#v", caller.Definition.Name, schema)
		}
	}
}
