package cron

import (
	"context"
	"errors"
	"testing"

	"elbot/internal/delivery"
)

func TestCanceledDeliveryStopsOnBusinessError(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	want := errors.New("send failed before cancellation")
	calls := 0
	service := newTestCronService(t, Options{Store: newFakeCronRepo(), SendTarget: func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, error) {
		calls++
		cancel()
		return delivery.Receipt{PlatformMessageIDs: []string{"partial"}}, want
	}})
	receipt, err := service.sendOutputsToPlatformTarget(ctx, "job", "cli", delivery.Target{Platform: "cli"}, []delivery.Output{delivery.Text("first"), delivery.Text("second")}, "")
	if calls != 1 || len(receipt.PlatformMessageIDs) != 1 || !errors.Is(err, want) || errors.Is(err, context.Canceled) {
		t.Fatalf("delivery termination changed: calls=%d receipt=%+v err=%v", calls, receipt, err)
	}
}
