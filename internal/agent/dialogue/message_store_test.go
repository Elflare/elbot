package dialogue

import (
	"context"
	"errors"
	"strings"
	"testing"

	"elbot/internal/session"
	"elbot/internal/storage"
)

type commitResultRepository struct {
	err error
}

func (r commitResultRepository) Commit(context.Context, storage.DialogueCommit) error {
	return r.err
}

type messageStoreGate struct{}

func (messageStoreGate) EnterSessions(ctx context.Context, _ ...string) (context.Context, func(), error) {
	return ctx, func() {}, nil
}

func (messageStoreGate) EnterBinding(ctx context.Context, _ *session.Binding) (context.Context, func(), error) {
	return ctx, func() {}, nil
}

func TestMessageCommitPreservesRepositoryError(t *testing.T) {
	want := errors.New("database write failed")
	store := &MessageStore{Dialogues: commitResultRepository{err: want}, Gate: &CommitGate{Sessions: messageStoreGate{}}}
	err := store.Commit(t.Context(), storage.DialogueCommit{SessionID: "session"}, "save reply")
	if !errors.Is(err, want) || !strings.Contains(err.Error(), "save reply") {
		t.Fatalf("repository error changed: %v", err)
	}
}
