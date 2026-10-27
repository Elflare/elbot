package headless

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/platform/share"
)

func TestStatelessLifecycle(t *testing.T) {
	a := New()
	if a.Name() != "service" {
		t.Fatalf("name = %q", a.Name())
	}
	var runtime share.Runtime = a
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	expired, stop := context.WithDeadline(t.Context(), time.Time{})
	defer stop()
	handler := func(context.Context, string, share.MessageContext) error {
		t.Error("headless delivered input")
		return nil
	}
	// 未启动即已无待清理资源，观察取消不覆盖完成结果。
	for _, ctx := range []context.Context{t.Context(), canceled, expired} {
		if err := runtime.Wait(ctx); err != nil {
			t.Fatal(err)
		}
		if err := runtime.Close(ctx); err != nil {
			t.Fatal(err)
		}
		if err := runtime.Start(ctx, handler); !errors.Is(err, ctx.Err()) {
			t.Fatalf("Start = %v, want %v", err, ctx.Err())
		}
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := runtime.Start(t.Context(), handler); err != nil {
				t.Error(err)
			}
			runtime.BeginClose()
			if err := runtime.Wait(canceled); err != nil {
				t.Error(err)
			}
			if err := runtime.Close(canceled); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
}

func TestSendingRequiresTargetPlatform(t *testing.T) {
	a := New()
	outputs := []delivery.Output{delivery.Text("hello")}
	for name, send := range map[string]func() (delivery.Receipt, events.LogResult, error){
		"chat": func() (delivery.Receipt, events.LogResult, error) {
			return a.SendChat(t.Context(), outputs)
		},
		"target": func() (delivery.Receipt, events.LogResult, error) {
			return a.SendTo(t.Context(), delivery.Target{Platform: "telegram", PrivateUserID: "user"}, outputs)
		},
	} {
		t.Run(name, func(t *testing.T) {
			receipt, result, err := send()
			const want = "service platform cannot send chat output without a target platform"
			if err == nil || err.Error() != want || result.ResultStatus != events.ResultFailed || result.Content != want {
				t.Fatalf("result = %+v, error = %v", result, err)
			}
			if !reflect.DeepEqual(receipt, delivery.Receipt{}) {
				t.Fatalf("unexpected receipt: %+v", receipt)
			}
		})
	}
}
