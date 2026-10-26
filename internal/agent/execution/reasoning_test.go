package execution

import (
	"context"

	"reflect"
	"testing"

	"elbot/internal/agent/dialogue"
	agentoutput "elbot/internal/agent/output"
	"elbot/internal/delivery"

	"elbot/internal/contextinfo"

	"elbot/internal/storage"
	"elbot/internal/turn"
)

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
	out := executionTurnOutput{view: &dialogue.ExecutionView{}, execution: execution, foreground: foregroundTurnOutput{sender: agentoutput.NewOutputSender(agentoutput.OutputSenderDeps{Reasoning: func(context.Context) delivery.ReasoningStream { return probe }})}}
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
