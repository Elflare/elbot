package headless

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"elbot/internal/delivery"
)

func TestStatelessLifecycle(t *testing.T) {
	a := New("service")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := a.Start(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start = %v", err)
	}
	if err := a.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestSendingRequiresTargetPlatform(t *testing.T) {
	receipt, err := New("service").SendTo(t.Context(), delivery.Target{}, []delivery.Output{delivery.Text("hello")})
	if err == nil || err.Error() != "service platform cannot send chat output without a target platform" {
		t.Fatalf("SendTo error = %v", err)
	}
	if !reflect.DeepEqual(receipt, delivery.Receipt{}) {
		t.Fatalf("unexpected receipt: %+v", receipt)
	}
}
