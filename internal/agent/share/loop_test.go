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
	state := share.NewLoopState(t.Context(), t.Context(), share.LoopInput{Session: &row, Selection: selected})
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

type oneRound struct {
	*dialogue.ToolExecutor
	*turn.Supervisor
	entries, runs int
}

func (r *oneRound) StartToolPhase(string, string) bool { r.entries++; return true }
func (r *oneRound) ExecuteWithCommitter(context.Context, *storage.Session, []llm.ToolCallRequest, string, string, string, share.Output, tool.ToolCommitter) tool.RunResult {
	r.runs++
	return tool.RunResult{}
}

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

func TestLoopToolLimitPairsSkippedResults(t *testing.T) {
	route := &oneRound{ToolExecutor: &dialogue.ToolExecutor{MaxRounds: 1}}
	deps := share.LoopDeps{Turns: route, Tools: route}
	state := share.NewLoopState(t.Context(), t.Context(), share.LoopInput{Session: &storage.Session{ID: "session"}})
	calls := []llm.ToolCallRequest{{ID: "a", Name: "first"}, {ID: "b", Name: "second"}}
	batch := new(skippedBatch)
	for _, wantLimit := range []bool{false, true} {
		r, limited := state.RunTools(deps, nil, calls, "", "", batch, func(int) string { return "skipped" })
		if r.Err != nil || r.Stopped || limited != wantLimit || route.entries != 1 || route.runs != 1 {
			t.Fatalf("limited=%t, run=%+v, entries=%d runs=%d", limited, r, route.entries, route.runs)
		}
	}
	if !batch.flushed || len(batch.calls) != 2 || !slices.Equal(batch.results, []string{"a:skipped", "b:skipped"}) {
		t.Fatalf("lost skipped call/result pairing: %+v", batch)
	}
}
