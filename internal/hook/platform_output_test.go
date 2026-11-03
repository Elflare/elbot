package hook

import (
	"context"
	"reflect"
	"testing"

	"elbot/internal/delivery"
	"elbot/internal/hook/pipeline"
	"elbot/internal/hook/share"
)

func TestPlatformConnectedResolvesOutputTarget(t *testing.T) {
	admins := delivery.Target{Platform: "telegram", Superadmins: true}
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "superadmins", true: "conflict"}[conflict], func(t *testing.T) {
			m, _ := newBridgeTestSupervisor(t)
			outputs := []delivery.Output{{Kind: delivery.KindText, Text: "online", Target: admins}}
			if conflict {
				outputs = append(outputs, delivery.Output{Kind: delivery.KindText, Target: delivery.Target{Platform: "cli", ChatType: "private", ChatID: "other"}})
			}
			m.manager = bridgeTestPipeline{run: func(_ context.Context, event share.Event, _ pipeline.CallOptions) (share.Event, error) {
				event.Outputs = outputs
				return event, nil
			}}
			calls := 0
			m.send = func(_ context.Context, target delivery.Target, sent []delivery.Output) (delivery.Receipt, error) {
				calls++
				if target != admins || !reflect.DeepEqual(sent, outputs) {
					t.Fatalf("target=%+v outputs=%+v", target, sent)
				}
				return delivery.Receipt{}, nil
			}
			m.handlePlatformConnected(t.Context(), "telegram")
			want := 1
			if conflict {
				want = 0
			}
			if calls != want {
				t.Fatalf("send calls=%d, want %d", calls, want)
			}
		})
	}
}
