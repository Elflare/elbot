package session

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"elbot/internal/storage"
)

type namingIOStore struct {
	storage.Store
	sessions *namingIOSessions
	messages *namingIOMessages
}

func (s namingIOStore) Sessions() storage.SessionRepository { return s.sessions }
func (s namingIOStore) Messages() storage.MessageRepository { return s.messages }

type namingIOSessions struct {
	storage.SessionRepository
	reads atomic.Int32
}

func (r *namingIOSessions) Get(ctx context.Context, id string) (*storage.Session, error) {
	r.reads.Add(1)
	return r.SessionRepository.Get(ctx, id)
}

type namingIOMessages struct {
	storage.MessageRepository
	reads   atomic.Int32
	started chan struct{}
	release chan struct{}
	err     error
}

func (r *namingIOMessages) ListBySession(ctx context.Context, id string) ([]storage.Message, error) {
	if r.reads.Add(1) == 1 && r.started != nil {
		close(r.started)
		<-r.release
	}
	if r.err != nil {
		return nil, r.err
	}
	return r.MessageRepository.ListBySession(ctx, id)
}

func TestNamingInactiveStatesSkipDatabase(t *testing.T) {
	for _, state := range []struct {
		name  string
		state namingState
	}{
		{"done", namingState{done: true}},
		{"in flight", namingState{inFlight: true}},
		{"failure limit", namingState{failures: maxNamingFailures}},
	} {
		t.Run(state.name, func(t *testing.T) {
			base := newTestStore(t)
			store := namingIOStore{base, &namingIOSessions{SessionRepository: base.Sessions()}, &namingIOMessages{MessageRepository: base.Messages()}}
			svc := NewServiceWithNaming(store, NamingConfig{TriggerStep: 1}, &fakeTitleGenerator{})
			startTestNaming(t, svc)
			row := namingRow(t, svc, base)
			svc.namingStates[row.ID] = state.state
			svc.MaybeScheduleNaming(t.Context(), row.ID)
			if store.sessions.reads.Load() != 0 || store.messages.reads.Load() != 0 {
				t.Fatalf("unnecessary reads: sessions=%d messages=%d", store.sessions.reads.Load(), store.messages.reads.Load())
			}
		})
	}
}

func TestNamingPreparationReservesSessionAndReleasesBelowThreshold(t *testing.T) {
	base := newTestStore(t)
	store := namingIOStore{base, &namingIOSessions{SessionRepository: base.Sessions()}, &namingIOMessages{
		MessageRepository: base.Messages(), started: make(chan struct{}), release: make(chan struct{}),
	}}
	generator := &fakeTitleGenerator{titles: []string{"generated"}, calls: make(chan []storage.Message, 1)}
	svc := NewServiceWithNaming(store, NamingConfig{TriggerStep: 2}, generator)
	startTestNaming(t, svc)
	row := namingRow(t, svc, base)
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(store.messages.release) }) })
	done := make(chan struct{})
	go func() { svc.MaybeScheduleNaming(t.Context(), row.ID); close(done) }()
	select {
	case <-store.messages.started:
	case <-time.After(time.Second):
		t.Fatal("preparation did not start")
	}
	svc.MaybeScheduleNaming(t.Context(), row.ID)
	if store.sessions.reads.Load() != 1 || store.messages.reads.Load() != 1 {
		t.Errorf("concurrent preparation reread the session: sessions=%d messages=%d", store.sessions.reads.Load(), store.messages.reads.Load())
	}
	release.Do(func() { close(store.messages.release) })
	<-done
	if err := base.Messages().Append(t.Context(), &storage.Message{SessionID: row.ID, Role: storage.RoleAssistant, Content: "answer"}); err != nil {
		t.Fatal(err)
	}
	svc.MaybeScheduleNaming(context.Background(), row.ID)
	if messages := waitGeneratorCall(t, generator.calls); len(messages) != 2 {
		t.Fatalf("naming input=%+v", messages)
	}
}

func TestNamingPreparationFailureReleasesSession(t *testing.T) {
	for _, name := range []string{"read failure", "canceled"} {
		t.Run(name, func(t *testing.T) {
			base := newTestStore(t)
			store := namingIOStore{base, &namingIOSessions{SessionRepository: base.Sessions()}, &namingIOMessages{MessageRepository: base.Messages()}}
			generator := &fakeTitleGenerator{titles: []string{"generated"}, calls: make(chan []storage.Message, 1)}
			svc := NewServiceWithNaming(store, NamingConfig{TriggerStep: 1}, generator)
			startTestNaming(t, svc)
			row := namingRow(t, svc, base)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if name == "canceled" {
				cancel()
			} else {
				store.messages.err = errors.New("read failed")
			}
			svc.MaybeScheduleNaming(ctx, row.ID)
			store.messages.err = nil
			svc.MaybeScheduleNaming(t.Context(), row.ID)
			if messages := waitGeneratorCall(t, generator.calls); len(messages) != 1 {
				t.Fatalf("naming retry input=%+v", messages)
			}
		})
	}
}

func TestManualNamingOnlyReadsSessionOnce(t *testing.T) {
	base := newTestStore(t)
	store := namingIOStore{base, &namingIOSessions{SessionRepository: base.Sessions()}, &namingIOMessages{MessageRepository: base.Messages()}}
	svc := NewServiceWithNaming(store, NamingConfig{TriggerStep: 1}, &fakeTitleGenerator{})
	startTestNaming(t, svc)
	row := namingRow(t, svc, base)
	if _, err := base.Sessions().Mutate(t.Context(), row.ID, func(row *storage.Session) error {
		row.Metadata = `{"title_renamed":true}`
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	svc.MaybeScheduleNaming(t.Context(), row.ID)
	svc.MaybeScheduleNaming(t.Context(), row.ID)
	if store.sessions.reads.Load() != 1 || store.messages.reads.Load() != 0 {
		t.Fatalf("manual title reads: sessions=%d messages=%d", store.sessions.reads.Load(), store.messages.reads.Load())
	}
}
