package rules

import (
	"context"
	"encoding/json"
	"testing"

	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/hook/share"
)

func TestOutputSendPassesResolvedTarget(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target Target
	}{
		{name: "current"},
		{name: "group", target: Target{Platform: "qqonebot", ChatType: "group", ChatID: "B"}},
		{name: "superadmins", target: Target{Platform: "telegram", Superadmins: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			module := Module{Opts: Options{Log: testLog, Notify: testNotify, Send: func(_ context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
				calls++
				if target != tc.target.Delivery() || len(outputs) != 1 || outputs[0].Text != "message" {
					t.Fatalf("target=%+v outputs=%+v", target, outputs)
				}
				return delivery.Receipt{}, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
			}}}
			params, err := json.Marshal(map[string]any{"target": tc.target, "outputs": []map[string]string{{"kind": "text", "text": "message"}}})
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := module.handleProtocolRequest(t.Context(), share.Event{}, Action{}, state{}, "output.send", params); err != nil || calls != 1 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}
