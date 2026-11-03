package share

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"elbot/internal/delivery"
	"elbot/internal/events"
)

func TestSuperadminsOwnsPlatformList(t *testing.T) {
	ids := []string{"first"}
	first := NewSuperadmins("first-platform", ids)
	second := NewSuperadmins("second-platform", []string{"second"})
	ids[0] = "second"
	if !first.IsSuperadmin("first") || first.IsSuperadmin("second") || second.IsSuperadmin("first") {
		t.Fatal("platform administrator lists leaked or were changed through caller storage")
	}
	if first.IsSuperadmin(" first ") || first.IsSuperadmin("first-platform:first") {
		t.Fatal("membership must compare native IDs exactly")
	}
}

func TestSuperadminsSendPreservesTargetsContextAndPartialReceipts(t *testing.T) {
	fault := errors.New("send failed")
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "partial failure"}[fail], func(t *testing.T) {
			admins := NewSuperadmins("injected-platform", []string{"first", "second", "third"})
			ctx := delivery.WithTemporaryConnection(t.Context())
			outputs := []delivery.Output{delivery.Text("notice")}
			var targets []delivery.Target
			receipt, result, err := admins.SendTo(ctx, outputs, func(gotCtx context.Context, target delivery.Target, gotOutputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
				if gotCtx != ctx || !delivery.UseTemporaryConnection(gotCtx) || !reflect.DeepEqual(outputs, gotOutputs) {
					t.Fatal("send context or outputs changed")
				}
				targets = append(targets, target)
				sent := delivery.Receipt{
					PlatformMessageIDs: []string{target.ChatID},
					SentMessages:       []delivery.SentMessage{{Platform: target.Platform, PlatformMessageID: target.ChatID}},
				}
				if fail && target.ChatID == "second" {
					return sent, events.LogResult{ResultStatus: events.ResultFailed, Content: "second failed"}, fault
				}
				return sent, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
			})
			wantIDs := []string{"first", "second", "third"}
			if fail {
				wantIDs = wantIDs[:2]
				if !errors.Is(err, fault) || result.ResultStatus != events.ResultFailed || result.Content != "second failed" {
					t.Fatalf("failure result=%+v err=%v", result, err)
				}
			} else if err != nil || result.ResultStatus != events.ResultSucceeded {
				t.Fatalf("success result=%+v err=%v", result, err)
			}
			if !reflect.DeepEqual(receipt.PlatformMessageIDs, wantIDs) || len(receipt.SentMessages) != len(wantIDs) || len(targets) != len(wantIDs) {
				t.Fatalf("targets=%+v receipt=%+v", targets, receipt)
			}
			for i, id := range wantIDs {
				if targets[i] != (delivery.Target{Platform: "injected-platform", ChatType: "private", ChatID: id}) {
					t.Fatalf("target contains inherited routing fields: %+v", targets[i])
				}
			}
		})
	}
}

func TestSuperadminsSendEmptyListAndCancellation(t *testing.T) {
	_, result, err := NewSuperadmins("injected-platform", nil).SendTo(t.Context(), nil,
		func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, events.LogResult, error) {
			t.Fatal("empty list invoked sender")
			return delivery.Receipt{}, events.LogResult{}, nil
		})
	if err == nil || err.Error() != "injected-platform superadmins are not configured" || result.ResultStatus != events.ResultFailed {
		t.Fatalf("empty list result=%+v err=%v", result, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	calls := 0
	_, result, err = NewSuperadmins("injected-platform", []string{"one", "two"}).SendTo(ctx, nil,
		func(ctx context.Context, _ delivery.Target, _ []delivery.Output) (delivery.Receipt, events.LogResult, error) {
			calls++
			return delivery.Receipt{}, events.LogResult{ResultStatus: events.ResultCanceled}, ctx.Err()
		})
	if !errors.Is(err, context.Canceled) || result.ResultStatus != events.ResultCanceled || calls != 1 {
		t.Fatalf("cancellation result=%+v err=%v calls=%d", result, err, calls)
	}
}
