package tool

import (
	"context"
	"elbot/internal/operationresult"
	"elbot/internal/testutil"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/tool/builtin"
	"elbot/internal/tool/chat_history"
	"elbot/internal/tool/share"
)

type historyScopeBackend struct {
	chat_history.Backend
	calls int
	query chat_history.SearchQuery
}

func (b *historyScopeBackend) Search(_ context.Context, query chat_history.SearchQuery) (operationresult.Result[[]share.Message], error) {
	b.calls++
	b.query = query
	return operationresult.Result[[]share.Message]{Status: operationresult.Success, Data: []share.Message{{Key: share.MessageKey{Platform: query.Platform, ChatID: query.ChatID, MessageID: "message", ChatType: query.ChatType}, SenderID: "AB12_OPENID", Text: "found"}}}, nil
}

func TestSearchToolScopeParameters(t *testing.T) {
	foreground := contextinfo.WithConversation(t.Context(), contextinfo.Conversation{Source: contextinfo.Source{Platform: "telegram", ChatType: "group", ChatID: "current"}})
	background := testutil.TaskContext(contextinfo.WithoutConversation(foreground), "cron")
	for _, tc := range []struct {
		name    string
		ctx     context.Context
		args    string
		wantErr string
	}{
		{"foreground default", foreground, `{}`, ""},
		{"foreground platform", foreground, `{"platform":"qqofficial"}`, "不能指定"},
		{"foreground scope", foreground, `{"chat_type":"private","chat_id":"AB12_OPENID"}`, "不能指定"},
		{"foreground empty", foreground, `{"platform":""}`, "不能指定"},
		{"foreground null", foreground, `{"chat_type":"private","chat_id":null}`, "不能指定"},
		{"standalone explicit", t.Context(), `{"platform":"qqofficial","chat_type":"private","chat_id":"AB12_OPENID","user":"AB12_OPENID"}`, ""},
		{"background absent", background, `{}`, "必须指定"},
		{"background missing scope", background, `{"platform":"qqofficial"}`, "必须指定"},
		{"background missing platform", background, `{"chat_type":"private","chat_id":"AB12_OPENID"}`, "必须指定"},
		{"background null", background, `{"platform":"qqofficial","chat_type":"private","chat_id":null}`, "必须指定"},
		{"background blank", background, `{"platform":" ","chat_type":"private","chat_id":"AB12_OPENID"}`, "必须指定"},
		{"background valid", background, `{"platform":"qqofficial","chat_type":"private","chat_id":"AB12_OPENID","user":"AB12_OPENID"}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := &historyScopeBackend{}
			tool := builtinTestTool(t, "search_chat_history", builtin.Deps{ChatHistory: chat_history.NewManager(backend, time.Now, resolveHistoryChat), ViewImageAvailable: func(context.Context) bool { return true }})
			tc.ctx = toolPolicyContext(tc.ctx)
			call, err := tool.PrepareCall(tc.ctx, share.CallRequest{Arguments: json.RawMessage(tc.args)})
			if err != nil || call.Status != operationresult.Success {
				t.Fatal(err)
			}
			result, _, err := call.Data.Call(tc.ctx)
			if tc.wantErr != "" {
				if err != nil || result.Status == operationresult.Success || !strings.Contains(result.Message, tc.wantErr) || backend.calls != 0 {
					t.Fatalf("result=%+v error=%v calls=%d", result, err, backend.calls)
				}
				return
			}
			if err != nil || result.Data == nil || backend.calls != 1 {
				t.Fatalf("result=%+v error=%v", result, err)
			}
			if share.PolicyFromContext(tc.ctx).ExplicitChat {
				if backend.query.Platform != "qqofficial" || (backend.query.ChatType != "private" || backend.query.ChatID != "AB12_OPENID") || backend.query.SenderQuery != "AB12_OPENID" {
					t.Fatalf("query=%+v", backend.query)
				}
				if !strings.Contains(result.Data.Content, "platform=qqofficial，chat_type=private，chat_id=AB12_OPENID") {
					t.Fatal(result.Data.Content)
				}
				for _, hint := range []string{"get_media(", "get_chat_history_around(", "reply_to_chat_history_message(", "view_image("} {
					if strings.Contains(result.Data.Content, hint) {
						t.Fatalf("background has current-chat hint: %s", result.Data.Content)
					}
				}
			} else if backend.query.Platform != "telegram" || (backend.query.ChatType != "group" || backend.query.ChatID != "current") || !strings.Contains(result.Data.Content, "get_chat_history_around(") {
				t.Fatalf("foreground query=%+v result=%s", backend.query, result.Data.Content)
			}
		})
	}
}

func TestSearchToolRejectsNonStringScopeArguments(t *testing.T) {
	tool := builtinTestTool(t, "search_chat_history", builtin.Deps{})
	for _, args := range []string{`{"platform":123}`, `{"chat_type":"private","chat_id":true}`, `{"chat_type":"private","chat_id":{}}`} {
		if result, err := tool.PrepareCall(t.Context(), share.CallRequest{Arguments: json.RawMessage(args)}); err != nil || result.Status == operationresult.Success {
			t.Fatalf("accepted %s", args)
		}
	}
}
