package output

import (
	"context"
	"reflect"
	"testing"

	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/signal"
)

func TestSendOutputsResolvesTargetBeforeDispatch(t *testing.T) {
	group := delivery.Target{Platform: "qqonebot", ChatType: "group", ChatID: "B"}
	admins := delivery.Target{Platform: "telegram", Superadmins: true}
	for _, tc := range []struct {
		name     string
		outputs  []delivery.Output
		want     delivery.Target
		conflict bool
	}{
		{name: "current", outputs: []delivery.Output{delivery.Text("current")}},
		{name: "group", outputs: []delivery.Output{delivery.Text("first"), {Kind: delivery.KindText, Text: "second", Target: group}}, want: group},
		{name: "superadmins", outputs: []delivery.Output{{Kind: delivery.KindText, Text: "online", Target: admins}}, want: admins},
		{name: "conflict", outputs: []delivery.Output{{Kind: delivery.KindText, Target: group}, {Kind: delivery.KindText, Target: admins}}, conflict: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probe := &backgroundDeliveryProbe{}
			sender := NewOutputSender(OutputSenderDeps{Dispatcher: probe})
			var records []events.LogRecord
			connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
				if record.EventName == "reply_delivered" {
					records = append(records, record)
				}
				return nil
			}, signal.ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Disconnect()
			_, err = sender.Send(t.Context(), delivery.StreamEvent{Type: delivery.StreamSendExtras, Outputs: tc.outputs})
			if tc.conflict {
				if err == nil || probe.calls != 0 || len(records) != 1 || records[0].ResultStatus != events.ResultFailed {
					t.Fatal("conflict: result= err= calls= logs=", err, probe.calls, records)
				}
				return
			}
			if err != nil || probe.calls != 1 || probe.target != tc.want || !reflect.DeepEqual(probe.outputs, tc.outputs) {
				t.Fatalf("target=%+v outputs=%+v calls=%d err=%v", probe.target, probe.outputs, probe.calls, err)
			}
		})
	}
}
