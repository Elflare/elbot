package execution

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"errors"
	"testing"

	"elbot/internal/llm"
	"elbot/internal/storage"
)

type failingRefreshDeps struct {
	runnerTestDeps
	err error
}

func (d *failingRefreshDeps) RefreshExecution(ctx context.Context, _ *storage.Session) (context.Context, error) {
	return ctx, d.err
}

func TestRunnerPreservesRefreshError(t *testing.T) {
	want := errors.New("execution state failed")
	deps := &failingRefreshDeps{err: want}
	result := NewRunner(NewManager(nil, nil)).Run(t.Context(), deps, toolshare.RunRequest{
		Session:   &storage.Session{ID: "session", Mode: storage.SessionModeWork},
		Calls:     []llm.ToolCallRequest{{ID: "call", Name: "unused"}},
		Committer: &testToolCommitter{},
	})
	if !errors.Is(result.Err, want) || result.Stopped {
		t.Fatalf("refresh error lost: %+v", result)
	}
}
