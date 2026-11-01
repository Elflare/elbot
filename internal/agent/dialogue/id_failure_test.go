package dialogue

import (
	"context"
	"errors"
	"testing"

	"elbot/internal/agent/share"
	"elbot/internal/storage"
)

func TestReplyAllocationFailurePrecedesOutputAndPersistence(t *testing.T) {
	want := errors.New("reserve failed")
	c := ReplyCommitter{NewMessageID: func(context.Context) (string, error) { return "", want }}
	// Nil output and persistence make any side effect before allocation fail the test.
	_, err := c.Commit(t.Context(), t.Context(), share.ReplyCommitInput{Session: &storage.Session{ID: "cl1"}, Text: "reply"}, nil)
	if !errors.Is(err, want) {
		t.Fatalf("error=%v", err)
	}
}
