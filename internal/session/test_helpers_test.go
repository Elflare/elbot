package session

import (
	"context"
	"elbot/internal/llm"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
	"fmt"
	"testing"
	"time"
)

func newTestService(t *testing.T) (*Service, storage.Store) {
	t.Helper()
	store := newTestStore(t)
	svc := NewService(store)
	svc.SetMaterials(testDisplayMaterials{})
	return svc, store
}

type testDisplayMaterials struct{}

func (testDisplayMaterials) MaterialFor(origin llm.Origin) (MaterialPreparer, error) {
	if origin.APIType != llm.APITypeChat {
		return nil, fmt.Errorf("native material is not configured in this display fixture")
	}
	return DisplayMaterial{}, nil
}

func newTestStore(t *testing.T) storage.Store {
	t.Helper()
	store, err := sqlite.New(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func startTestNaming(t *testing.T, svc *Service) {
	t.Helper()
	svc.StartNaming(context.Background())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := svc.Close(ctx); err != nil {
			t.Error(err)
		}
	})
}

type storeWithSessionRepository struct {
	storage.Store
	sessions storage.SessionRepository
}

func (s storeWithSessionRepository) Sessions() storage.SessionRepository { return s.sessions }

type failingGetSessionRepository struct {
	storage.SessionRepository
	err error
}

func (r failingGetSessionRepository) Get(context.Context, string) (*storage.Session, error) {
	return nil, r.err
}

type fakeTitleGenerator struct {
	titles []string
	errs   []error
	calls  chan []storage.Message
}

func (g *fakeTitleGenerator) GenerateTitle(ctx context.Context, messages []storage.Message) (TitleResult, error) {
	select {
	case g.calls <- append([]storage.Message(nil), messages...):
	case <-ctx.Done():
		return TitleResult{}, ctx.Err()
	}
	title := ""
	if len(g.titles) > 0 {
		title = g.titles[0]
		g.titles = g.titles[1:]
	}
	var err error
	if len(g.errs) > 0 {
		err = g.errs[0]
		g.errs = g.errs[1:]
	}
	return TitleResult{RawTitle: title}, err
}

type fakeNamingNotifier struct {
	failures chan NamingFailedEvent
}

func (n *fakeNamingNotifier) connect(t *testing.T, svc *Service) {
	t.Helper()
	c, err := svc.NamingSignals().Failed.Connect(func(ctx context.Context, event NamingFailedEvent) error {
		select {
		case n.failures <- event:
		case <-ctx.Done():
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Disconnect)
}

func waitTitle(t *testing.T, store storage.Store, sessionID, want string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		session, err := store.Sessions().Get(context.Background(), sessionID)
		if err != nil {
			t.Fatalf("get session: %v", err)
		}
		if session.Title == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("title did not become %q", want)
}

func waitGeneratorCall(t *testing.T, ch <-chan []storage.Message) []storage.Message {
	t.Helper()
	select {
	case messages := <-ch:
		return messages
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for title generator")
		return nil
	}
}

func ensureNoGeneratorCall(t *testing.T, ch <-chan []storage.Message) {
	t.Helper()
	select {
	case messages := <-ch:
		t.Fatalf("unexpected title generator call: %#v", messages)
	case <-time.After(50 * time.Millisecond):
	}
}

func sessionIDs(sessions []storage.SessionSummary) map[string]bool {
	ids := map[string]bool{}
	for _, session := range sessions {
		ids[session.ID] = true
	}
	return ids
}
