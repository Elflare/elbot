package chat_history

import (
	"context"
	"elbot/internal/operationresult"
	"elbot/internal/tool/share"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"elbot/internal/contextinfo"
)

func TestChatHistoryUsesPublicSource(t *testing.T) {
	ctx := contextinfo.WithConversation(context.Background(), contextinfo.Conversation{
		Source:   contextinfo.Source{Platform: "qqofficial", ChatType: "group", ChatID: "openid"},
		Identity: contextinfo.Identity{ID: "user"},
	})
	chat, err := currentChatHistoryContext(ctx)
	if err != nil || chat.Status != operationresult.Success || chat.Data.Platform != "qqofficial" || (chat.Data.ChatType != "group" || chat.Data.ChatID != "openid") {
		t.Fatalf("chat = %+v, err = %v", chat, err)
	}
	if result, err := currentChatHistoryContext(context.Background()); err != nil || result.Status != operationresult.NoCurrentSession {
		t.Fatal("missing source must not invent a history scope")
	}
}

type queryBackend struct {
	Backend
	search SearchQuery
	around AroundQuery
	key    share.MessageKey
	calls  int
}

func (b *queryBackend) Search(_ context.Context, q SearchQuery) (operationresult.Result[[]share.Message], error) {
	b.calls++
	b.search = q
	return operationresult.Result[[]share.Message]{Status: operationresult.Success, Data: []share.Message{{Text: "search result"}}}, nil
}

func (b *queryBackend) Around(_ context.Context, q AroundQuery) (operationresult.Result[[]share.Message], error) {
	b.calls++
	b.around = q
	return operationresult.Result[[]share.Message]{Status: operationresult.Success, Data: nil}, nil
}

func (b *queryBackend) GetMessage(_ context.Context, key share.MessageKey) (operationresult.Result[share.Message], error) {
	b.calls++
	b.key = key
	return operationresult.Result[share.Message]{Status: operationresult.NotFound, Message: "not found"}, nil
}

func TestManagerQueriesStayInConversation(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.Local)
	backend := &queryBackend{}
	m := NewManager(backend, func() time.Time { return now }, func(ctx context.Context, _ share.SearchRequest) (operationresult.Result[contextinfo.Source], error) {
		info, _ := contextinfo.ConversationFromContext(ctx)
		return operationresult.Result[contextinfo.Source]{Status: operationresult.Success, Data: info.Source}, nil
	})
	ctx := contextinfo.WithConversation(t.Context(), contextinfo.Conversation{
		Source:   contextinfo.Source{Platform: "qqofficial", ChatType: "group", ChatID: "one"},
		Identity: contextinfo.Identity{ID: "user"},
	})
	rows, err := m.Search(ctx, share.SearchRequest{Query: " ice,火，snow | wind ", QueryMode: "or", User: "我", Hours: 2, Days: 7, Limit: 100})
	if err != nil || rows.Status != operationresult.Success || len(rows.Data) != 1 || rows.Data[0].Text != "search result" {
		t.Fatalf("Search = %+v, %v", rows, err)
	}
	q := backend.search
	if q.Platform != "qqofficial" || (q.ChatType != "group" || q.ChatID != "one") || q.SenderQuery != "我" || q.Limit != 50 || q.QueryMode != "or" {
		t.Fatalf("query = %+v", q)
	}
	if !reflect.DeepEqual(q.QueryTerms, []string{"ice", "火", "snow", "wind"}) || q.Since == nil || !q.Since.Equal(now.Add(-2*time.Hour)) || q.Until != nil {
		t.Fatalf("terms/time = %+v", q)
	}
	_, err = m.Around(ctx, share.AroundRequest{MessageID: "42", Before: -1, After: 500})
	wantKey := share.MessageKey{Platform: "qqofficial", ChatType: "group", ChatID: "one", MessageID: "42"}
	if err != nil || backend.around != (AroundQuery{Key: wantKey, Before: 0, After: 50}) {
		t.Fatalf("Around = %+v, %v", backend.around, err)
	}
	if result, err := m.GetMessage(ctx, "42"); err != nil || result.Status != operationresult.NotFound || backend.key != wantKey {
		t.Fatalf("GetMessage = %+v, %v", backend.key, err)
	}
}

func TestManagerRejectsMissingContextBeforeBackend(t *testing.T) {
	backend := &queryBackend{}
	m := NewManager(backend, time.Now, func(ctx context.Context, _ share.SearchRequest) (operationresult.Result[contextinfo.Source], error) {
		if err := ctx.Err(); err != nil {
			return operationresult.Result[contextinfo.Source]{}, err
		}
		info, ok := contextinfo.ConversationFromContext(ctx)
		if !ok {
			return operationresult.Result[contextinfo.Source]{Status: operationresult.NoCurrentSession, Message: "missing chat context"}, nil
		}
		return operationresult.Result[contextinfo.Source]{Status: operationresult.Success, Data: info.Source}, nil
	})
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name   string
		ctx    context.Context
		want   error
		status operationresult.Status
	}{
		{"missing", t.Context(), nil, operationresult.NoCurrentSession},
		{"canceled", canceled, context.Canceled, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if result, err := m.Search(tc.ctx, share.SearchRequest{}); !errors.Is(err, tc.want) || result.Status != tc.status {
				t.Fatal(err)
			}
			if result, err := m.Around(tc.ctx, share.AroundRequest{MessageID: "42"}); !errors.Is(err, tc.want) || result.Status != tc.status {
				t.Fatal(err)
			}
			if result, err := m.GetMessage(tc.ctx, "42"); !errors.Is(err, tc.want) || result.Status != tc.status {
				t.Fatal(err)
			}
		})
	}
	if backend.calls != 0 {
		t.Fatalf("backend called %d times", backend.calls)
	}
}

func TestSearchOpaqueUserIDIsNotOnlyNickname(t *testing.T) {
	backend := &queryBackend{}
	m := NewManager(backend, time.Now, func(ctx context.Context, _ share.SearchRequest) (operationresult.Result[contextinfo.Source], error) {
		info, ok := contextinfo.ConversationFromContext(ctx)
		if !ok {
			return operationresult.Result[contextinfo.Source]{Status: operationresult.NoCurrentSession, Message: "missing chat context"}, nil
		}
		return operationresult.Result[contextinfo.Source]{Status: operationresult.Success, Data: info.Source}, nil
	})
	ctx := contextinfo.WithConversation(t.Context(), contextinfo.Conversation{
		Source: contextinfo.Source{Platform: "qqofficial", ChatType: "private", ChatID: "AB12_OPENID"},
	})
	if _, err := m.Search(ctx, share.SearchRequest{User: "AB12_OPENID"}); err != nil {
		t.Fatal(err)
	}
	if backend.search.SenderQuery != "AB12_OPENID" {
		t.Fatalf("openid changed: %+v", backend.search)
	}
}

func TestSearchUserIsAlwaysLiteral(t *testing.T) {
	backend := &queryBackend{}
	m := NewManager(backend, time.Now, func(ctx context.Context, _ share.SearchRequest) (operationresult.Result[contextinfo.Source], error) {
		info, ok := contextinfo.ConversationFromContext(ctx)
		if !ok {
			return operationresult.Result[contextinfo.Source]{Status: operationresult.NoCurrentSession, Message: "missing chat context"}, nil
		}
		return operationresult.Result[contextinfo.Source]{Status: operationresult.Success, Data: info.Source}, nil
	})
	ctx := contextinfo.WithConversation(t.Context(), contextinfo.Conversation{
		Source:   contextinfo.Source{Platform: "qqofficial", ChatType: "private", ChatID: "AB12_OPENID"},
		Identity: contextinfo.Identity{ID: "must-not-replace-user"},
	})
	for _, user := range []string{"AB12_OPENID", "123456", "Alice", "芙蕾娅", "@AB12_OPENID", "我", "本人", "自己", "  padded  ", "  "} {
		if _, err := m.Search(ctx, share.SearchRequest{User: user}); err != nil {
			t.Fatal(err)
		}
		if backend.search.SenderQuery != strings.TrimSpace(user) {
			t.Fatalf("user=%q query=%q", user, backend.search.SenderQuery)
		}
	}
}
