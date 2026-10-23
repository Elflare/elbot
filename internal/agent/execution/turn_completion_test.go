package execution

import (
	"context"
	"errors"
	"testing"
	"time"

	"elbot/internal/agent/dialogue"

	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/llm/chatcompletions"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

type turnCompletionStore struct {
	storage.Store
	sessions *turnCompletionSessions
}

func TestProviderUsageIsVisibleWhileResponseHookWaits(t *testing.T) {
	base := newTestStore(t)
	tokens := int64(43)
	model := &fakeLLM{chunks: [][]chatcompletions.Chunk{{{DeltaContent: "raw", Usage: &llm.Usage{TotalTokens: &tokens}}}}}
	f := newExecutionFixture(t, model, base)
	ctx, requestCtx, in, _ := f.begin(t)
	entered, release := make(chan struct{}), make(chan struct{})
	f.on(t, hook.PointLLMResponseReceived, func(event hook.Event) hook.Event {
		close(entered)
		<-release
		changed := int64(999)
		event.LLM.Usage = &llm.Usage{TotalTokens: &changed}
		return event
	})
	done := make(chan dialogue.TurnResult, 1)
	go func() { done <- f.runner.RunTurn(ctx, requestCtx, in, f.out) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("Hook did not start")
	}
	usage, err := f.execution.status.Usage(in.Session)
	close(release)
	if err != nil || usage == nil || usage.TotalTokens == nil || *usage.TotalTokens != tokens {
		t.Fatalf("provider usage delayed: %+v %v", usage, err)
	}
	select {
	case result := <-done:
		if result.Err != nil {
			t.Fatal(result.Err)
		}
	case <-time.After(time.Second):
		t.Fatal("turn did not finish")
	}
	usage, _ = f.execution.status.Usage(in.Session)
	if *usage.TotalTokens != tokens {
		t.Fatal("response Hook overwrote provider fact")
	}
}

func (s turnCompletionStore) Sessions() storage.SessionRepository { return s.sessions }

type turnCompletionSessions struct {
	storage.SessionRepository
	err error
}

func (r *turnCompletionSessions) Mutate(ctx context.Context, id string, update func(*storage.Session) error) (*storage.Session, error) {
	if r.err != nil {
		return nil, r.err
	}
	return r.SessionRepository.Mutate(ctx, id, update)
}

func TestTurnCompletionHandlesPendingAndSaveFailure(t *testing.T) {
	for _, name := range []string{"usage", "nil usage", "pending", "save failure"} {
		t.Run(name, func(t *testing.T) {
			base := newTestStore(t)
			repo := &turnCompletionSessions{SessionRepository: base.Sessions()}
			f := newExecutionFixture(t, &fakeLLM{}, turnCompletionStore{base, repo})
			ctx, _, in, _ := f.begin(t)
			result := dialogue.TurnResult{Outcome: dialogue.Completed, Selection: in.Selection, Usage: &llm.Usage{TotalTokens: new(int64)}}
			*result.Usage.TotalTokens = 7
			if name == "nil usage" {
				result.Usage = nil
			}
			if name == "pending" {
				f.opts.Turns.StartToolPhase(in.Session.ID, "attempt")
				f.opts.Turns.AppendPendingInput(in.Session.ID, turn.Input{Text: "next"})
			}
			if name == "save failure" {
				repo.err = errors.New("database unavailable")
			}
			pending, err := f.execution.finishCompletedTurn(ctx, in.Session, f.out, result)
			if name == "save failure" {
				if !errors.Is(err, repo.err) || !f.opts.Turns.MatchesAttempt(in.Session.ID, "attempt") {
					t.Fatalf("save failure advanced completion: pending=%+v error=%v", pending, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if name == "pending" && (pending.Text != "next" || f.opts.Turns.Snapshot(in.Session.ID).Phase == turn.PhaseIdle) {
				t.Fatalf("lost pending continuation: %+v", pending)
			}
		})
	}
}

func TestToolLoopCompletionKeepsLatestUsage(t *testing.T) {
	base := newTestStore(t)
	providerTokens := int64(17)
	model := &fakeLLM{chunks: [][]chatcompletions.Chunk{
		{{ToolCallDeltas: []chatcompletions.ToolCallDelta{{ID: "first", Name: "prepared_args", Args: `{}`}}, FinishReason: "tool_calls"}},
		{{ToolCallDeltas: []chatcompletions.ToolCallDelta{{ID: "second", Name: "prepared_args", Args: `{}`}}, FinishReason: "tool_calls"}},
		{{DeltaContent: "done", Usage: &llm.Usage{TotalTokens: &providerTokens}}},
	}}
	f := newExecutionFixture(t, model, base)
	var arguments string
	if err := f.opts.ToolRegistry.Register(preparedArgumentTool{arguments: &arguments}); err != nil {
		t.Fatal(err)
	}
	ctx, requestCtx, in, _ := f.begin(t)
	calls := int64(0)
	f.on(t, hook.PointLLMResponseReceived, func(event hook.Event) hook.Event {
		calls++
		tokens := calls
		event.LLM.Usage = &llm.Usage{TotalTokens: &tokens}
		return event
	})
	result := f.runner.RunTurn(ctx, requestCtx, in, f.out)
	if result.Err != nil || result.Outcome != dialogue.Completed || calls != 3 || arguments != "{}" {
		t.Fatalf("loop result=%+v calls=%d tool arguments=%q", result, calls, arguments)
	}
	if _, err := f.execution.finishCompletedTurn(ctx, in.Session, f.out, result); err != nil {
		t.Fatal(err)
	}
	latest, err := base.Sessions().Get(t.Context(), in.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	usage, err := f.execution.status.Usage(latest)
	if err != nil || usage == nil || usage.TotalTokens == nil || *usage.TotalTokens != providerTokens {
		t.Fatalf("last call usage=%+v error=%v", usage, err)
	}
	if f.opts.Turns.Snapshot(in.Session.ID).Phase != turn.PhaseIdle {
		t.Fatal("completed Turn did not become idle")
	}
}
