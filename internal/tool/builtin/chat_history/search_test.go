package chat_history

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/operationresult"
	"elbot/internal/storage"
	"reflect"
	"strings"
	"testing"
	"time"
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

type queryRepo struct {
	storage.ChatHistoryRepository
	search storage.ChatHistorySearchRequest
}

func (r *queryRepo) SearchChatHistory(_ context.Context, q storage.ChatHistorySearchRequest) ([]storage.ChatMessage, error) {
	r.search = q
	return []storage.ChatMessage{{Text: "search result"}}, nil
}
func TestSearchAppliesTimeRangeAndLiteralUser(t *testing.T) {
	r := &queryRepo{}
	d := Deps{Repository: r}
	ctx := contextinfo.WithConversation(t.Context(), contextinfo.Conversation{Source: contextinfo.Source{Platform: "qqofficial", ChatType: "group", ChatID: "one"}})
	for _, user := range []string{"AB12_OPENID", "123456", "Alice", "芙蕾娅", "@AB12_OPENID", "我", "本人", "自己", " padded ", " "} {
		res, err := d.search(ctx, searchChatHistoryArgs{Query: "ice,火，snow | wind", User: user, Hours: 2, Days: 7, Limit: 100}, searchScope{})
		q := r.search
		if err != nil || res.Status != operationresult.Success || q.ChatID != "one" || q.SenderQuery != strings.TrimSpace(user) || q.Limit != 50 || q.Since == nil || time.Since(*q.Since) < 2*time.Hour || time.Since(*q.Since) > 2*time.Hour+time.Minute || !reflect.DeepEqual(q.QueryTerms, []string{"ice", "火", "snow", "wind"}) {
			t.Fatalf("%+v %v", q, err)
		}
	}
}
func TestForegroundSearchRejectsIncompleteConversation(t *testing.T) {
	for _, source := range []contextinfo.Source{{Platform: "qqofficial"}, {ChatID: "group"}} {
		repo := &queryRepo{}
		ctx := contextinfo.WithConversation(t.Context(), contextinfo.Conversation{Source: source})
		got, err := (Deps{Repository: repo}).search(ctx, searchChatHistoryArgs{}, searchScope{})
		if err != nil || got.Status != operationresult.NoCurrentSession || repo.search.Limit != 0 {
			t.Fatalf("source=%+v status=%s query=%+v err=%v", source, got.Status, repo.search, err)
		}
	}
}
func TestCanceledHistoryContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := currentChatHistoryContext(ctx); err != context.Canceled {
		t.Fatal(err)
	}
}
