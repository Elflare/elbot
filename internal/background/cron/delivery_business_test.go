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
	service := newTestCronService(t, Options{Store: newFakeCronRepo(), SendTarget: func(context.Context, delivery.Target, []delivery.Output) (receiptResult, error) {
		calls++
		cancel()
		return receiptResult{}, want
	}})
	receipt, err := service.sendDeliveryOutput(ctx, "job", delivery.Target{Platform: "cli"}, delivery.Text("first"), "")
	if calls != 1 || receipt.Status != "" || !errors.Is(err, want) || errors.Is(err, context.Canceled) {
		t.Fatalf("delivery termination changed: calls=%d receipt=%+v err=%v", calls, receipt, err)
	}
}

func TestDeliveryCanceledBeforeSend(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	service := newTestCronService(t, Options{SendTarget: func(context.Context, delivery.Target, []delivery.Output) (receiptResult, error) {
		t.Fatal("canceled delivery reached the platform")
		return sentReceipt(delivery.Receipt{}), nil
	}})
	receipt, err := service.sendDeliveryOutput(ctx, "job", delivery.Target{Platform: "cli"}, delivery.Text("report"), "")
	if !errors.Is(err, context.Canceled) || receipt.Status != "" || len(receipt.Data.PlatformMessageIDs) != 0 || len(receipt.Data.SentMessages) != 0 {
		t.Fatalf("canceled delivery: receipt=%+v err=%v", receipt, err)
	}
}
