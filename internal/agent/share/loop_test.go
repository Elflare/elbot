package share_test

import (
	"context"
	"slices"
	"testing"

	"elbot/internal/agent/dialogue"
	"elbot/internal/agent/share"
	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/operationresult"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/turn"
)

type loopSession struct{ row storage.Session }

func (s *loopSession) Get(context.Context, string) (*storage.Session, error) { return &s.row, nil }

func TestLoopRefreshAcrossTakeover(t *testing.T) {
	live := &loopSession{row: storage.Session{ID: "session", Mode: storage.SessionModeBackground}}
	row := live.row
	selected := modelmgr.Selection{Origin: llm.Origin{APIType: llm.APITypeChat, Provider: "test"}}
	selected.Model = "background"
	state := share.LoopState{LoopInput: share.LoopInput{Session: &row, Selection: selected}, Ctx: t.Context(), RequestCtx: t.Context()}
	selections, refreshes := 0, 0
	deps := share.LoopDeps{View: dialogue.ExecutionView{Sessions: live}, SelectionForTurn: func(context.Context, *storage.Session) modelmgr.Selection {
		selections++
		selected.Model = "foreground"
		return selected
	}}
	check := func(modelmgr.Selection) operationresult.Result[struct{}] {
		return operationresult.Result[struct{}]{Status: operationresult.Success}
	}
	refresh := func() error {
		refreshes++
		for _, ctx := range []context.Context{state.Ctx, state.RequestCtx} {
			model, _ := contextinfo.ModelFromContext(ctx)
			if model.Model != "foreground" {
				t.Fatalf("stale foreground materials: %+v", model)
			}
		}
		return nil
	}
	for i := 0; i < 3; i++ {
		if i == 1 {
			live.row.Mode = storage.SessionModeWork
			live.row.Metadata = `{"foreground_origin":{"mode":"background"}}`
		}
		checked, err := state.RefreshSession(deps, check, refresh)
		if err != nil || checked.Status != operationresult.Success {
			t.Fatalf("round %d: %+v, %v", i, checked, err)
		}
		want := min(i, 1)
		if selections != want || refreshes != want {
			t.Fatalf("round %d: selected %d, rebuilt %d", i, selections, refreshes)
		}
	}
}

// No attempt owns the tool phase. Only the route that skips before entering it may commit.
type refusedToolPhase struct{ *turn.Supervisor }

func (refusedToolPhase) StartToolPhase(string, string) bool { return false }

type skippedBatch struct {
	tool.ToolCommitter
	calls   []storage.ToolCall
	results []string
	flushed bool
}

func (b *skippedBatch) Begin(_ context.Context, head *storage.Message) error {
	b.calls = head.ToolCalls
	return nil
}
func (b *skippedBatch) Result(_ context.Context, _ int, _ llm.ToolCallRequest, _ llm.LLMMessage, u *storage.ToolResultCommit) error {
	b.results = append(b.results, u.Result.ToolCallID+":"+u.Result.Content)
	return nil
}
func (b *skippedBatch) Flush(context.Context) error { b.flushed = true; return nil }

func TestLoopToolLimitPreservesPhaseBoundary(t *testing.T) {
	deps := share.LoopDeps{Turns: refusedToolPhase{}, Tools: &dialogue.ToolExecutor{MaxRounds: 0}}
	calls := []llm.ToolCallRequest{{ID: "a", Name: "first"}, {ID: "b", Name: "second"}}
	for _, enterOnLimit := range []bool{true, false} {
		state := share.LoopState{LoopInput: share.LoopInput{Session: &storage.Session{ID: "session"}}, Ctx: t.Context(), RequestCtx: t.Context()}
		batch := new(skippedBatch)
		r, limited := state.RunTools(deps, nil, calls, "", "", batch, enterOnLimit, func(int) string { return "skipped" })
		if r.Err != nil || r.Stopped != enterOnLimit || limited == enterOnLimit {
			t.Fatalf("enterOnLimit=%t: %+v, limited=%t", enterOnLimit, r, limited)
		}
		if enterOnLimit {
			if len(batch.calls) != 0 || batch.flushed {
				t.Fatal("expired attempt committed a batch")
			}
		} else if !batch.flushed || len(batch.calls) != 2 || !slices.Equal(batch.results, []string{"a:skipped", "b:skipped"}) {
			t.Fatalf("lost skipped call/result pairing: %+v", batch)
		}
	}
}
