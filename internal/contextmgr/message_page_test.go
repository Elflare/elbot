package contextmgr

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
	"fmt"
	"testing"
	"time"
)

type pageOnlyRepository struct {
	storage.MessageRepository
	read int
}

func (p *pageOnlyRepository) ListBySession(context.Context, string) ([]storage.Message, error) {
	panic("unbounded history read")
}
func (p *pageOnlyRepository) ListBySessionUpTo(context.Context, string, string) ([]storage.Message, error) {
	panic("unbounded parent read")
}
func (p *pageOnlyRepository) ListConversationMessages(ctx context.Context, id, until string, limit, offset int) ([]storage.Message, error) {
	rows, err := p.MessageRepository.ListConversationMessages(ctx, id, until, limit, offset)
	p.read += len(rows)
	return rows, err
}

type pageOnlyStore struct {
	storage.Store
	messages *pageOnlyRepository
}

func (s pageOnlyStore) Messages() storage.MessageRepository { return s.messages }

func TestMessagePageIsBoundedAndIncludesForkHistoryBeforeSummary(t *testing.T) {
	store, err := sqlite.New(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	parent := storage.Session{ID: "parent", Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "u"}, Platform: "cli", ChatID: "local", ChatType: "private"}
	if err := store.Sessions().Create(t.Context(), &parent); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 8; i++ {
		m := storage.Message{ID: fmt.Sprintf("p-%d", i), SessionID: parent.ID, Role: storage.RoleUser, Content: fmt.Sprintf("parent %d", i), CreatedAt: at}
		if err := store.Messages().Append(t.Context(), &m); err != nil {
			t.Fatal(err)
		}
	}
	child := storage.Session{ID: "child", Owner: parent.Owner, Platform: "cli", ChatID: "local", ChatType: "private", ParentSessionID: parent.ID, ForkFromMessageID: "p-4"}
	if err := store.Sessions().Create(t.Context(), &child); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		m := storage.Message{ID: fmt.Sprintf("c-%d", i), SessionID: child.ID, Role: storage.RoleAssistant, Content: "child", CreatedAt: at}
		if err := store.Messages().Append(t.Context(), &m); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ContextSummaries().Create(t.Context(), &storage.ContextSummary{SessionID: child.ID, ToMessageID: "c-1", Summary: "compressed"}); err != nil {
		t.Fatal(err)
	}
	repo := &pageOnlyRepository{MessageRepository: store.Messages()}
	loader := Loader{Store: pageOnlyStore{Store: store, messages: repo}}
	for _, tc := range []struct {
		page int
		ids  []string
	}{{1, []string{"c-0", "c-1", "c-2"}}, {2, []string{"p-2", "p-3", "p-4"}}, {3, []string{"p-0", "p-1"}}} {
		repo.read = 0
		got, err := loader.LoadMessagePage(t.Context(), child.ID, tc.page, 3)
		if err != nil {
			t.Fatal(err)
		}
		if got.TotalPages != 3 || len(got.Messages) != len(tc.ids) || repo.read > 3 {
			t.Fatalf("page=%+v read=%d", got, repo.read)
		}
		for i, id := range tc.ids {
			if got.Messages[i].ID != id {
				t.Fatalf("messages=%+v", got.Messages)
			}
		}
	}
}

func TestMessagePageCutoffCanBelongToEarlierAncestor(t *testing.T) {
	store, err := sqlite.New(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, row := range []storage.Session{
		{ID: "root", Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "u"}, Platform: "cli", ChatID: "local", ChatType: "private"},
		{ID: "middle", Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "u"}, Platform: "cli", ChatID: "local", ChatType: "private", ParentSessionID: "root", ForkFromMessageID: "r-2"},
		{ID: "leaf", Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "u"}, Platform: "cli", ChatID: "local", ChatType: "private", ParentSessionID: "middle", ForkFromMessageID: "r-1"},
	} {
		if err := store.Sessions().Create(t.Context(), &row); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range []storage.Message{{ID: "r-1", SessionID: "root", Role: storage.RoleUser, Content: "first"}, {ID: "r-2", SessionID: "root", Role: storage.RoleAssistant, Content: "later root"}, {ID: "m-1", SessionID: "middle", Role: storage.RoleUser, Content: "later middle"}, {ID: "l-1", SessionID: "leaf", Role: storage.RoleAssistant, Content: "leaf"}} {
		if err := store.Messages().Append(t.Context(), &m); err != nil {
			t.Fatal(err)
		}
	}
	repo := &pageOnlyRepository{MessageRepository: store.Messages()}
	got, err := (Loader{Store: pageOnlyStore{Store: store, messages: repo}}).LoadMessagePage(t.Context(), "leaf", 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 2 || got.Messages[0].ID != "r-1" || got.Messages[1].ID != "l-1" || repo.read != 2 {
		t.Fatalf("page=%+v read=%d", got, repo.read)
	}
}
