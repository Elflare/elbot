package session

import (
	"context"
	"elbot/internal/contextinfo"
	"errors"
	"testing"

	"elbot/internal/storage"
)

func TestSessionTakenOverFactsAndMissing(t *testing.T) {
	s, store := newTestService(t)
	for _, row := range []*storage.Session{
		{ID: "ordinary", Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "owner"}, Platform: "cli", ChatID: "cli", Mode: storage.SessionModeWork, Status: storage.SessionStatusActive, ChatType: "private"},
		{ID: "promoted", Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "owner"}, Platform: "cli", ChatID: "cli", Mode: storage.SessionModeWork, Status: storage.SessionStatusActive, Metadata: `{"foreground_origin":{"kind":"cron"}}`, ChatType: "private"},
	} {
		if err := store.Sessions().Create(t.Context(), row); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"", "missing", "ordinary", "promoted"} {
		taken, err := s.SessionTakenOver(t.Context(), id)
		if err != nil || taken != (id == "promoted") {
			t.Fatalf("%q=%v,%v", id, taken, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.SessionTakenOver(ctx, "ordinary"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled lookup=%v", err)
	}
}

type takeoverQueryStore struct {
	storage.Store
	repo storage.SessionRepository
}

func (s takeoverQueryStore) Sessions() storage.SessionRepository { return s.repo }

type takeoverQueryRepo struct {
	storage.SessionRepository
	err error
}

func (r takeoverQueryRepo) Get(context.Context, string) (*storage.Session, error) { return nil, r.err }

func TestSessionTakenOverPropagatesStorageFailure(t *testing.T) {
	want := errors.New("read failed")
	s := newTestSupervisor(t, takeoverQueryStore{Store: newTestStore(t), repo: takeoverQueryRepo{err: want}}, nil)
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	if taken, err := s.SessionTakenOver(t.Context(), "session"); taken || !errors.Is(err, want) {
		t.Fatalf("lookup=%v,%v", taken, err)
	}
}
