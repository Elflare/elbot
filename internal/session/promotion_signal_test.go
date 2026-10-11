package session

import (
	"context"
	"errors"
	"testing"

	"elbot/internal/signal"
	"elbot/internal/storage"
)

type failingPromotionRepository struct {
	storage.SessionRepository
	err error
}

func (r failingPromotionRepository) Mutate(context.Context, string, func(*storage.Session) error) (*storage.Session, error) {
	return nil, r.err
}

func TestForegroundSignalFollowsCommitAndIsEmittedOnce(t *testing.T) {
	s, store := newTestService(t)
	defer s.Close(context.Background())
	scope := Scope{ActorID: "cli:local", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	row, err := s.PrepareBackground(t.Context(), scope, BackgroundRequest{Kind: "cron", Name: "job"})
	if err != nil {
		t.Fatal(err)
	}
	events := make(chan ForegroundActivatedEvent, 2)
	c, err := s.ForegroundActivated().Observe(func(_ context.Context, event ForegroundActivatedEvent) error {
		events <- event
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Disconnect()
	failure := errors.New("commit failed")
	s.store = storeWithSessionRepository{Store: store, sessions: failingPromotionRepository{store.Sessions(), failure}}
	if _, err := s.Resume(t.Context(), scope, row.ID); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatal("notified before successful commit")
	}
	s.store = store
	promoted, err := s.Resume(t.Context(), scope, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-events:
		if event.Session.Metadata != promoted.Metadata || event.Session.Mode != storage.SessionModeWork || !event.Binding.Valid() || event.Binding.SessionID() != row.ID {
			t.Fatal("incomplete committed handoff", event)
		}
	default:
		t.Fatal("resume returned before notification")
	}
	if _, err := s.Resume(t.Context(), scope, row.ID); err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatal("ordinary resume emitted another promotion")
	}
}

func TestForegroundSignalFailureDoesNotHideCommittedPromotion(t *testing.T) {
	s, store := newTestService(t)
	defer s.Close(context.Background())
	scope := Scope{ActorID: "cli:local", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	row, err := s.PrepareBackground(t.Context(), scope, BackgroundRequest{Kind: "cron"})
	if err != nil {
		t.Fatal(err)
	}
	want := errors.New("receiver failed")
	c, err := s.ForegroundActivated().Observe(func(context.Context, ForegroundActivatedEvent) error { return want }, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Disconnect()
	promoted, err := s.Resume(t.Context(), scope, row.ID)
	if !errors.Is(err, want) || promoted == nil {
		t.Fatal(promoted, err)
	}
	stored, err := store.Sessions().Get(t.Context(), row.ID)
	if err != nil || IsBackground(stored) {
		t.Fatal("committed promotion was lost", stored, err)
	}
}
