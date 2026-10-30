package output

import (
	"context"
	"log/slog"
	"reflect"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/events"
)

type backgroundDeliveryProbe struct {
	calls   int
	target  delivery.Target
	outputs []delivery.Output
}

func (p *backgroundDeliveryProbe) SendTo(_ context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
	p.calls++
	p.target = target
	p.outputs = outputs
	return delivery.Receipt{}, events.LogResult{}, nil
}

func TestAssistantActionsSurviveTextHook(t *testing.T) {
	probe := &backgroundDeliveryProbe{}
	hooks := &previewTestHooks{rewrite: true, output: "正文完全改写"}
	out := NewOutputSender(OutputSenderDeps{Dispatcher: probe, Hooks: hooks})
	actions := []delivery.Action{{ID: "confirm", Label: "确认", Command: "/confirm"}}
	if _, _, err := out.SendAssistant(t.Context(), "original", actions...); err != nil {
		t.Fatal(err)
	}
	if probe.calls != 1 || len(probe.outputs) != 1 || probe.outputs[0].Text != hooks.output || !reflect.DeepEqual(probe.outputs[0].Actions, actions) {
		t.Fatalf("output lost text or actions: %#v", probe.outputs)
	}
	if _, _, err := out.SendAssistant(t.Context(), "高风险工具调用等待确认 /confirm"); err != nil {
		t.Fatal(err)
	}
	if len(probe.outputs[0].Actions) != 0 {
		t.Fatal("plain text acquired actions")
	}
}
func (p *backgroundDeliveryProbe) SendChat(ctx context.Context, outputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
	return p.SendTo(ctx, delivery.Target{}, outputs)
}
func (p *backgroundDeliveryProbe) StartStream(context.Context) (delivery.MessageStream, error) {
	p.calls++
	return nil, nil
}

func TestBackgroundOutputIsSilentExceptExplicitDelivery(t *testing.T) {
	probe := &backgroundDeliveryProbe{}
	hooks := &previewTestHooks{}
	out := NewOutputSender(OutputSenderDeps{Dispatcher: probe, Hooks: hooks, Reasoning: func(context.Context) delivery.ReasoningStream { probe.calls++; return nil }})
	ctx := contextinfo.WithBackgroundKind(t.Context(), contextinfo.BackgroundKindCron)
	if _, err := out.SendOutputs(ctx, []delivery.Output{delivery.Text("ordinary")}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := out.SendAssistant(ctx, "assistant"); err != nil {
		t.Fatal(err)
	}
	if err := out.SendTo(ctx, delivery.Target{}, []delivery.Output{delivery.Text("default")}); err != nil {
		t.Fatal(err)
	}
	if stream, err := out.StartStream(ctx); err != nil || stream != nil {
		t.Fatal(stream, err)
	}
	if out.StartReasoning(ctx) != nil {
		t.Fatal("reasoning stream started")
	}
	out.Preview(ctx, "preview")
	out.Notify(ctx, slog.LevelInfo, "notice")
	if probe.calls != 0 || hooks.calls != 0 {
		t.Fatal("background output escaped", probe.calls, hooks.calls)
	}
	target := delivery.Target{Platform: "test", GroupID: "destination"}
	if err := out.SendTo(ctx, target, []delivery.Output{delivery.Text("explicit")}); err != nil {
		t.Fatal(err)
	}
	if probe.calls != 1 || probe.target != target {
		t.Fatal("explicit delivery suppressed", probe)
	}
}
