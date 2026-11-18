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

func TestRuleCardInputCanRetryAfterFailedCommit(t *testing.T) {
	want := errors.New("database write failed")
	store := &MessageStore{Dialogues: commitResultRepository{err: want}, Gate: &CommitGate{Sessions: messageStoreGate{}}}
	row := &storage.Session{ID: "s", Metadata: `{"keep":1}`}
	user := &storage.Message{SessionID: row.ID, Content: "rules and input"}
	input := storage.DialogueCommit{SessionID: row.ID, Messages: []*storage.Message{user}, ElyphRulesShown: true}
	persistence := store.Committer("append_user_message")
	if err := commitInput(t.Context(), row, input, persistence); !errors.Is(err, want) || row.Metadata != `{"keep":1}` {
		t.Fatalf("failed write advanced session or lost error: metadata=%s err=%v", row.Metadata, err)
	}
	store.Dialogues = commitResultRepository{}
	if err := commitInput(t.Context(), row, input, persistence); err != nil {
		t.Fatal(err)
	}
	if row.Metadata != `{"elyph_rules_shown":true,"keep":1}` || user.Metadata != `{"elyph_rules_shown":true}` {
		t.Fatalf("successful retry: session=%s message=%s", row.Metadata, user.Metadata)
	}
}
func TestMessageCommitPreservesRepositoryError(t *testing.T) {
	want := errors.New("database write failed")
	store := &MessageStore{Dialogues: commitResultRepository{err: want}, Gate: &CommitGate{Sessions: messageStoreGate{}}}
	err := store.Commit(t.Context(), storage.DialogueCommit{SessionID: "session"}, "save reply")
	if !errors.Is(err, want) || !strings.Contains(err.Error(), "save reply") {
		t.Fatalf("repository error changed: %v", err)
	}
}
