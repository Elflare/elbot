package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"elbot/internal/agent/dialogue"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/llm/chatcompletions"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

type reasoningProbe struct {
	deltas      []string
	finished    bool
	afterAppend func()
}

func (p *reasoningProbe) Append(_ context.Context, text string) {
	p.deltas = append(p.deltas, text)
	if p.afterAppend != nil {
		p.afterAppend()
	}
}
func (p *reasoningProbe) Finish(context.Context) { p.finished = true }

func TestChatReasoningUsesRawDeltasAndClosesOnEveryOutcome(t *testing.T) {
	for _, outcome := range []string{"success", "failure", "cancel"} {
		t.Run(outcome, func(t *testing.T) {
			chunks := []chatcompletions.Chunk{{DeltaReasoningContent: "first"}, {DeltaReasoningContent: "second"}, {DeltaContent: "answer"}}
			if outcome == "failure" {
				chunks = append(chunks[:2], chatcompletions.Chunk{Error: errors.New("provider failed")})
			}
			a := newTestAgent(t, &fakePlatform{}, &fakeLLM{chunks: [][]chatcompletions.Chunk{chunks}}, "test-model", config.ProviderConfig{}, newTestStore(t))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			probe := &reasoningProbe{}
			if outcome == "cancel" {
				probe.afterAppend = cancel
			}
			a.output.reasoning = func(context.Context) dialogue.ReasoningStream { return probe }
			err := a.HandleMessage(ctx, "hello")
			if outcome == "success" && err != nil {
				t.Fatal(err)
			}
			if !probe.finished || len(probe.deltas) == 0 || probe.deltas[0] != "first" {
				t.Fatalf("raw reasoning or finish lost: %+v", probe)
			}
			if outcome != "cancel" && !reflect.DeepEqual(probe.deltas, []string{"first", "second"}) {
				t.Fatal(probe.deltas)
			}
		})
	}
}

type adoptionReasoningProbe struct {
	targets  []string
	canceled bool
}

func (p *adoptionReasoningProbe) Append(ctx context.Context, text string) {
	info, _ := contextinfo.ConversationFromContext(ctx)
	if target, ok := info.PlatformData.(string); ok {
		p.targets = append(p.targets, target+":"+text)
	}
	p.canceled = ctx.Err() != nil
}
func (*adoptionReasoningProbe) Finish(context.Context) {}

func TestExecutionReasoningResolvesAdoptionForNewDeltas(t *testing.T) {
	execution := turn.NewExecution("reasoning-adoption")
	ctx, cancel := context.WithCancel(turn.WithExecution(context.Background(), execution))
	defer cancel()
	probe := &adoptionReasoningProbe{}
	out := executionTurnOutput{execution: execution, foreground: foregroundTurnOutput{sender: &OutputSender{reasoning: func(context.Context) dialogue.ReasoningStream { return probe }}}}
	stream := out.StartReasoning(ctx)
	stream.Append(ctx, "background")
	execution.Adopt(contextinfo.WithConversation(context.Background(), contextinfo.Conversation{PlatformData: "original"}), storage.Session{})
	stream.Append(ctx, "new")
	if !reflect.DeepEqual(probe.targets, []string{"original:new"}) {
		t.Fatalf("adoption replayed or lost deltas: %v", probe.targets)
	}
	cancel()
	stream.Append(ctx, "canceled")
	if !probe.canceled {
		t.Fatal("adoption discarded request cancellation")
	}
	stream.Finish(ctx)
}

func TestResponsesReasoningUsesRawDeltasAndClosesOnEveryOutcome(t *testing.T) {
	for _, outcome := range []string{"success", "failure", "cancel"} {
		t.Run(outcome, func(t *testing.T) {
			f := newNativeFixture(t, func(_ int, _ nativeTestRequest, w http.ResponseWriter) {
				fmt.Fprint(w, "event: response.reasoning_text.delta\ndata: {\"type\":\"response.reasoning_text.delta\",\"delta\":\"first\"}\n\n")
				fmt.Fprint(w, "event: response.reasoning_summary_text.delta\ndata: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"second\"}\n\n")
				if outcome == "failure" {
					fmt.Fprint(w, "event: error\ndata: {\"type\":\"error\",\"code\":\"server_error\",\"message\":\"failed\"}\n\n")
					return
				}
				emitNative(w, "reasoning-result", "completed", nativeText("answer"))
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			probe := &reasoningProbe{}
			if outcome == "cancel" {
				probe.afterAppend = cancel
			}
			f.agent.output.reasoning = func(context.Context) dialogue.ReasoningStream { return probe }
			err := f.agent.HandleMessage(ctx, "hello")
			if outcome == "success" && err != nil {
				t.Fatal(err)
			}
			if !probe.finished || len(probe.deltas) == 0 || probe.deltas[0] != "first" {
				t.Fatalf("lost reasoning or finish: %+v", probe)
			}
			if outcome != "cancel" && !reflect.DeepEqual(probe.deltas, []string{"first", "second"}) {
				t.Fatal(probe.deltas)
			}
		})
	}
}
