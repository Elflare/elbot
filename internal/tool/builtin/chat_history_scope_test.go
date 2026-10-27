package builtin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/tool/chat_history"
	"elbot/internal/tool/share"
)

type historyScopeBackend struct {
	chat_history.Backend
	calls int
	query chat_history.SearchQuery
}

func (b *historyScopeBackend) Search(_ context.Context, query chat_history.SearchQuery) ([]share.Message, error) {
	b.calls++
	b.query = query
	return []share.Message{{Key: share.MessageKey{Platform: query.Platform, ScopeID: query.ScopeID, MessageID: "message"}, SenderID: "AB12_OPENID", Text: "found"}}, nil
}

func TestSearchToolScopeParameters(t *testing.T) {
	foreground := contextinfo.WithConversation(t.Context(), contextinfo.Conversation{Source: contextinfo.Source{Platform: "telegram", ScopeID: "group:current"}})
	background := contextinfo.WithBackgroundKind(foreground, contextinfo.BackgroundKindCron)
	for _, tc := range []struct {
		name    string
		ctx     context.Context
		args    string
		wantErr string
	}{
		{"foreground default", foreground, `{}`, ""},
		{"foreground platform", foreground, `{"platform":"qqofficial"}`, "不能指定"},
		{"foreground scope", foreground, `{"scope":"c2c:AB12_OPENID"}`, "不能指定"},
		{"foreground empty", foreground, `{"platform":""}`, "不能指定"},
		{"foreground null", foreground, `{"scope":null}`, "不能指定"},
		{"background absent", background, `{}`, "必须指定"},
		{"background missing scope", background, `{"platform":"qqofficial"}`, "必须指定"},
		{"background missing platform", background, `{"scope":"c2c:AB12_OPENID"}`, "必须指定"},
		{"background null", background, `{"platform":"qqofficial","scope":null}`, "必须指定"},
		{"background blank", background, `{"platform":" ","scope":"c2c:AB12_OPENID"}`, "必须指定"},
		{"background valid", background, `{"platform":" qqofficial ","scope":" c2c:AB12_OPENID ","user":"AB12_OPENID"}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := &historyScopeBackend{}
			tool := NewSearchChatHistoryTool(chat_history.NewManager(backend, time.Now), func(context.Context) bool { return true })
			call, err := tool.PrepareCall(tc.ctx, share.CallRequest{Arguments: json.RawMessage(tc.args)})
			if err != nil {
				t.Fatal(err)
			}
			result, _, err := call.Call(tc.ctx)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || backend.calls != 0 {
					t.Fatalf("result=%+v error=%v calls=%d", result, err, backend.calls)
				}
				return
			}
			if err != nil || result == nil || backend.calls != 1 {
				t.Fatalf("result=%+v error=%v", result, err)
			}
			if contextinfo.BackgroundContext(tc.ctx) {
				if backend.query.Platform != "qqofficial" || backend.query.ScopeID != "c2c:AB12_OPENID" || backend.query.SenderQuery != "AB12_OPENID" {
					t.Fatalf("query=%+v", backend.query)
				}
				if !strings.Contains(result.Content, "platform=qqofficial，scope=c2c:AB12_OPENID") {
					t.Fatal(result.Content)
				}
				for _, hint := range []string{"get_media(", "get_chat_history_around(", "reply_to_chat_history_message(", "view_image("} {
					if strings.Contains(result.Content, hint) {
						t.Fatalf("background has current-chat hint: %s", result.Content)
					}
				}
			} else if backend.query.Platform != "telegram" || backend.query.ScopeID != "group:current" || !strings.Contains(result.Content, "get_chat_history_around(") {
				t.Fatalf("foreground query=%+v result=%s", backend.query, result.Content)
			}
		})
	}
}

func TestSearchToolRejectsNonStringScopeArguments(t *testing.T) {
	tool := NewSearchChatHistoryTool(nil, nil)
	for _, args := range []string{`{"platform":123}`, `{"scope":true}`, `{"scope":{}}`} {
		if _, err := tool.PrepareCall(t.Context(), share.CallRequest{Arguments: json.RawMessage(args)}); err == nil {
			t.Fatalf("accepted %s", args)
		}
	}
}
