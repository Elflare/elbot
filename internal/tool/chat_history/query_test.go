package chat_history

import (
	"context"
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
		Source:   contextinfo.Source{Platform: "qqofficial", ScopeID: "group:openid"},
		Identity: contextinfo.Identity{ActorID: "qqofficial:user", PlatformUserID: "user"},
	})
	chat, err := currentChatHistoryContext(ctx)
	if err != nil || chat.Platform != "qqofficial" || chat.ScopeID != "group:openid" {
		t.Fatalf("chat = %+v, err = %v", chat, err)
	}
	if _, err := currentChatHistoryContext(context.Background()); err == nil {
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

func (b *queryBackend) Search(_ context.Context, q SearchQuery) ([]share.Message, error) {
	b.calls++
	b.search = q
	return []share.Message{{Text: "search result"}}, nil
}

func (b *queryBackend) Around(_ context.Context, q AroundQuery) ([]share.Message, error) {
	b.calls++
	b.around = q
	return nil, nil
}

func (b *queryBackend) GetMessage(_ context.Context, key share.MessageKey) (share.Message, error) {
	b.calls++
	b.key = key
	return share.Message{}, share.ErrNotFound
}

func TestManagerQueriesStayInConversation(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.Local)
	backend := &queryBackend{}
	m := NewManager(backend, func() time.Time { return now })
	ctx := contextinfo.WithConversation(t.Context(), contextinfo.Conversation{
		Source:   contextinfo.Source{Platform: "qqofficial", ScopeID: "group:one"},
		Identity: contextinfo.Identity{PlatformUserID: "user"},
	})
	rows, err := m.Search(ctx, share.SearchRequest{Query: " ice,火，snow | wind ", QueryMode: "or", User: "我", Hours: 2, Days: 7, Limit: 100})
	if err != nil || len(rows) != 1 || rows[0].Text != "search result" {
		t.Fatalf("Search = %+v, %v", rows, err)
	}
	q := backend.search
	if q.Platform != "qqofficial" || q.ScopeID != "group:one" || q.SenderQuery != "我" || q.Limit != 50 || q.QueryMode != "or" {
		t.Fatalf("query = %+v", q)
	}
	if !reflect.DeepEqual(q.QueryTerms, []string{"ice", "火", "snow", "wind"}) || q.Since == nil || !q.Since.Equal(now.Add(-2*time.Hour)) || q.Until != nil {
		t.Fatalf("terms/time = %+v", q)
	}
	_, err = m.Around(ctx, share.AroundRequest{MessageID: "42", Before: -1, After: 500})
	wantKey := share.MessageKey{Platform: "qqofficial", ScopeID: "group:one", MessageID: "42"}
	if err != nil || backend.around != (AroundQuery{Key: wantKey, Before: 0, After: 50}) {
		t.Fatalf("Around = %+v, %v", backend.around, err)
	}
	if _, err = m.GetMessage(ctx, "42"); !errors.Is(err, share.ErrNotFound) || backend.key != wantKey {
		t.Fatalf("GetMessage = %+v, %v", backend.key, err)
	}
}

func TestManagerRejectsMissingContextBeforeBackend(t *testing.T) {
	backend := &queryBackend{}
	m := NewManager(backend, time.Now)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		want error
	}{
		{"missing", t.Context(), share.ErrNoChatContext},
		{"canceled", canceled, context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := m.Search(tc.ctx, share.SearchRequest{}); !errors.Is(err, tc.want) {
				t.Fatal(err)
			}
			if _, err := m.Around(tc.ctx, share.AroundRequest{MessageID: "42"}); !errors.Is(err, tc.want) {
				t.Fatal(err)
			}
			if _, err := m.GetMessage(tc.ctx, "42"); !errors.Is(err, tc.want) {
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
	m := NewManager(backend, time.Now)
	ctx := contextinfo.WithConversation(t.Context(), contextinfo.Conversation{
		Source: contextinfo.Source{Platform: "qqofficial", ScopeID: "c2c:AB12_OPENID"},
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
	m := NewManager(backend, time.Now)
	ctx := contextinfo.WithConversation(t.Context(), contextinfo.Conversation{
		Source:   contextinfo.Source{Platform: "qqofficial", ScopeID: "c2c:AB12_OPENID"},
		Identity: contextinfo.Identity{PlatformUserID: "must-not-replace-user"},
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

func TestSearchExplicitScopeRules(t *testing.T) {
	platform, scope, empty, blank := " qqofficial ", " c2c:AB12_OPENID ", "", "  "
	foreground := contextinfo.WithConversation(t.Context(), contextinfo.Conversation{
		Source: contextinfo.Source{Platform: "telegram", ScopeID: "group:current"},
	})
	for _, tc := range []struct {
		name string
		ctx  context.Context
		req  share.SearchRequest
		want string
	}{
		{"foreground platform", foreground, share.SearchRequest{Platform: &platform}, "前台"},
		{"foreground scope", foreground, share.SearchRequest{Scope: &scope}, "前台"},
		{"foreground both", foreground, share.SearchRequest{Platform: &platform, Scope: &scope}, "前台"},
		{"foreground empty", foreground, share.SearchRequest{Platform: &empty}, "前台"},
		{"background missing", contextinfo.WithBackgroundKind(foreground, contextinfo.BackgroundKindCron), share.SearchRequest{}, "必须指定"},
		{"background no scope", contextinfo.WithBackgroundKind(foreground, contextinfo.BackgroundKindCron), share.SearchRequest{Platform: &platform}, "必须指定"},
		{"background no platform", contextinfo.WithBackgroundKind(foreground, contextinfo.BackgroundKindElnis), share.SearchRequest{Scope: &scope}, "必须指定"},
		{"background blank scope", contextinfo.WithBackgroundKind(foreground, ""), share.SearchRequest{Platform: &platform, Scope: &blank}, "必须指定"},
		{"background empty platform", contextinfo.WithBackgroundKind(foreground, ""), share.SearchRequest{Platform: &empty, Scope: &scope}, "必须指定"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := &queryBackend{}
			_, err := NewManager(backend, time.Now).Search(tc.ctx, tc.req)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v", err)
			}
			if backend.calls != 0 {
				t.Fatal("invalid scope reached backend")
			}
		})
	}
	for _, kind := range []contextinfo.BackgroundKind{contextinfo.BackgroundKindCron, contextinfo.BackgroundKindElnis, ""} {
		for _, parent := range []context.Context{t.Context(), foreground} {
			backend := &queryBackend{}
			ctx := contextinfo.WithBackgroundKind(parent, kind)
			_, err := NewManager(backend, time.Now).Search(ctx, share.SearchRequest{Platform: &platform, Scope: &scope})
			if err != nil || backend.search.Platform != "qqofficial" || backend.search.ScopeID != "c2c:AB12_OPENID" {
				t.Fatalf("query=%+v error=%v", backend.search, err)
			}
		}
	}
	backend := &queryBackend{}
	ctx, cancel := context.WithCancel(contextinfo.WithBackgroundKind(t.Context(), contextinfo.BackgroundKindCron))
	cancel()
	_, err := NewManager(backend, time.Now).Search(ctx, share.SearchRequest{Platform: &platform, Scope: &scope})
	if err != context.Canceled || backend.calls != 0 {
		t.Fatalf("canceled search: %v, calls=%d", err, backend.calls)
	}
}
