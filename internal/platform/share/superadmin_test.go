package share

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"elbot/internal/delivery"
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
			receipt, err := admins.SendTo(ctx, outputs, func(gotCtx context.Context, target delivery.Target, gotOutputs []delivery.Output) (delivery.Receipt, error) {
				if gotCtx != ctx || !delivery.UseTemporaryConnection(gotCtx) || !reflect.DeepEqual(outputs, gotOutputs) {
					t.Fatal("send context or outputs changed")
				}
				targets = append(targets, target)
				sent := delivery.Receipt{
					PlatformMessageIDs: []string{target.ChatID},
					SentMessages:       []delivery.SentMessage{{Platform: target.Platform, PlatformMessageID: target.ChatID}},
				}
				if fail && target.ChatID == "second" {
					return sent, fault
				}
				return sent, nil
			})
			wantIDs := []string{"first", "second", "third"}
			if fail {
				wantIDs = wantIDs[:2]
				if !errors.Is(err, fault) {
					t.Fatal("failure result= err=", err)
				}
			} else if err != nil {
				t.Fatal("success result= err=", err)
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
	_, err := NewSuperadmins("injected-platform", nil).SendTo(t.Context(), nil,
		func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, error) {
			t.Fatal("empty list invoked sender")
			return delivery.Receipt{}, nil
		})
	if err == nil || err.Error() != "injected-platform superadmins are not configured" {
		t.Fatal("empty list result= err=", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	calls := 0
	_, err = NewSuperadmins("injected-platform", []string{"one", "two"}).SendTo(ctx, nil,
		func(ctx context.Context, _ delivery.Target, _ []delivery.Output) (delivery.Receipt, error) {
			calls++
			return delivery.Receipt{}, ctx.Err()
		})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatal("cancellation result= err= calls=", err, calls)
	}
}
