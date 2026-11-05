package output

import (
	"context"
	"elbot/internal/testutil"
	"log/slog"
	"reflect"
	"testing"

	"elbot/internal/delivery"
)

type backgroundDeliveryProbe struct {
	calls   int
	target  delivery.Target
	outputs []delivery.Output
}

func (p *backgroundDeliveryProbe) SendTo(_ context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, error) {
	p.calls++
	p.target = target
	p.outputs = outputs
	return delivery.Receipt{}, nil
}

func TestAssistantActionsSurviveTextHook(t *testing.T) {
	probe := &backgroundDeliveryProbe{}
	hooks := &previewTestHooks{rewrite: true, output: "正文完全改写"}
	out := NewOutputSender(OutputSenderDeps{Dispatcher: probe, Hooks: hooks})
	actions := []delivery.Action{{ID: "confirm", Label: "确认", Command: "/confirm"}}
	if _, err := out.Send(t.Context(), delivery.StreamEvent{Type: delivery.StreamSend, Text: "original", Actions: actions}); err != nil {
		t.Fatal(err)
	}
	if probe.calls != 1 || len(probe.outputs) != 1 || probe.outputs[0].Text != hooks.output || !reflect.DeepEqual(probe.outputs[0].Actions, actions) {
		t.Fatalf("output lost text or actions: %#v", probe.outputs)
	}
	if _, err := out.Send(t.Context(), delivery.StreamEvent{Type: delivery.StreamSend, Text: "高风险工具调用等待确认 /confirm"}); err != nil {
		t.Fatal(err)
	}
	if len(probe.outputs[0].Actions) != 0 {
		t.Fatal("plain text acquired actions")
	}
}
func (p *backgroundDeliveryProbe) StartStream(context.Context) (delivery.MessageStream, error) {
	return delivery.StreamFunc(func(ctx context.Context, event delivery.StreamEvent) (delivery.Receipt, error) {
		if event.Type != delivery.StreamSend {
			return delivery.Receipt{}, nil
		}
		return p.SendTo(ctx, delivery.Target{}, []delivery.Output{{Kind: delivery.KindText, Text: event.Text, Actions: event.Actions}})
	}), nil
}

func TestBackgroundOutputIsSilentExceptExplicitDelivery(t *testing.T) {
	probe := &backgroundDeliveryProbe{}
	hooks := &previewTestHooks{}
	out := NewOutputSender(OutputSenderDeps{Dispatcher: probe, Hooks: hooks})
	ctx := testutil.TaskContext(t.Context(), "cron")
	if _, err := out.Send(ctx, delivery.StreamEvent{Type: delivery.StreamSendExtras, Outputs: []delivery.Output{delivery.Text("ordinary")}}); err != nil {
		t.Fatal(err)
	}
	if _, err := out.Send(ctx, delivery.StreamEvent{Type: delivery.StreamSend, Text: "assistant"}); err != nil {
		t.Fatal(err)
	}
	if err := out.SendTo(ctx, delivery.Target{}, []delivery.Output{delivery.Text("default")}); err != nil {
		t.Fatal(err)
	}
	out.Preview(ctx, "preview")
	out.Notify(ctx, slog.LevelInfo, "notice")
	if probe.calls != 0 || hooks.calls != 0 {
		t.Fatal("background output escaped", probe.calls, hooks.calls)
	}
	target := delivery.Target{Platform: "test", ChatType: "group", ChatID: "destination"}
	if err := out.SendTo(ctx, target, []delivery.Output{delivery.Text("explicit")}); err != nil {
		t.Fatal(err)
	}
	if probe.calls != 1 || probe.target != target {
		t.Fatal("explicit delivery suppressed", probe)
	}
}
