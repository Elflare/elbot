package dialogue

import (
	"context"
	"errors"
	"testing"

	"elbot/internal/llm"
	"elbot/internal/storage"
	"elbot/internal/tool"
)

type failingToolState struct {
	*tool.Supervisor
	err error
}

func (s failingToolState) DiscoveredToolsForSession(context.Context, string, bool) ([]tool.DiscoveredTool, error) {
	return nil, s.err
}

type unenteredToolCommitter struct {
	tool.ToolCommitter
	t *testing.T
}

func (c unenteredToolCommitter) Begin(context.Context, *storage.Message) error {
	c.t.Fatal("tool state preparation failed but a tool transcript was started")
	return nil
}

func TestToolStateFailureStopsBeforeTranscript(t *testing.T) {
	want := errors.New("tool state unavailable")
	executor := &ToolExecutor{Manager: failingToolState{err: want}}
	result := executor.ExecuteWithCommitter(t.Context(), &storage.Session{ID: "session", Mode: storage.SessionModeWork}, []llm.ToolCallRequest{{ID: "call", Name: "tool"}}, "", "", nil, unenteredToolCommitter{t: t})
	if result.Err != want {
		t.Fatalf("preparation failure changed: %+v", result)
	}
}
