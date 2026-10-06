package dispatch

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/platform"
)

type testSender struct {
	send func(context.Context, delivery.Notice) (delivery.Receipt, error)
}

func (s testSender) Name() string                                        { return "primary" }
func (s testSender) Run(context.Context, platform.PlatformHandler) error { return nil }
func (s testSender) SendChat(ctx context.Context, outputs []delivery.Output) (delivery.Receipt, error) {
	return s.send(ctx, delivery.Notice{Outputs: outputs})
}
func (s testSender) SendNotice(ctx context.Context, notice delivery.Notice) (delivery.Receipt, error) {
	return s.send(ctx, notice)
}

func TestRouterPreservesConcurrentSourceSnapshots(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]contextinfo.Conversation{}
	sender := testSender{send: func(ctx context.Context, notice delivery.Notice) (delivery.Receipt, error) {
		info, ok := contextinfo.ConversationFromContext(ctx)
		if !ok {
			return delivery.Receipt{}, errors.New("missing source")
		}
		mu.Lock()
		seen[notice.Outputs[0].Text] = info
		mu.Unlock()
		return delivery.Receipt{}, nil
	}}
	router := New(Options{})
	router.RegisterPlatformSender("test", sender)
	var wg sync.WaitGroup
	for i := range 24 {
		id := fmt.Sprint(i)
		info := contextinfo.Conversation{Source: contextinfo.Source{Platform: "test", ScopeID: id}, PlatformMessageID: "message-" + id, PlatformData: id}
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := contextinfo.WithConversation(context.Background(), info)
			if _, err := router.SendNotice(ctx, delivery.Notice{Outputs: []delivery.Output{delivery.Text(id)}}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(seen) != 24 {
		t.Fatalf("received %d snapshots", len(seen))
	}
	for id, info := range seen {
		if info.Source.ScopeID != id || info.PlatformMessageID != "message-"+id || info.PlatformData != id {
			t.Fatalf("mixed source: %#v", info)
		}
	}
}

func TestRouterExplicitTargetOverridesSenderAndKeepsPartialReceipt(t *testing.T) {
	wantErr := errors.New("second output failed")
	primaryCalls, overrideCalls := 0, 0
	primary := testSender{send: func(_ context.Context, n delivery.Notice) (delivery.Receipt, error) {
		primaryCalls++
		if n.Target.PrivateUserID != "other" {
			t.Fatalf("target lost: %#v", n.Target)
		}
		return delivery.Receipt{PlatformMessageIDs: []string{"sent"}}, wantErr
	}}
	override := testSender{send: func(_ context.Context, n delivery.Notice) (delivery.Receipt, error) {
		overrideCalls++
		if !n.Target.Empty() {
			t.Fatal("default target became explicit")
		}
		return delivery.Receipt{}, nil
	}}
	router := New(Options{Primary: primary})
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{Sender: override})
	if _, err := router.SendNotice(ctx, delivery.Notice{Outputs: []delivery.Output{delivery.Text("reply")}}); err != nil {
		t.Fatal(err)
	}
	got, err := router.SendNotice(ctx, delivery.Notice{Target: delivery.Target{PrivateUserID: "other"}, Outputs: []delivery.Output{delivery.Text("explicit")}})
	if !errors.Is(err, wantErr) || len(got.PlatformMessageIDs) != 1 || primaryCalls != 1 || overrideCalls != 1 {
		t.Fatalf("receipt=%#v error=%v calls=%d/%d", got, err, primaryCalls, overrideCalls)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := router.SendChat(cancelled, []delivery.Output{delivery.Text("cancelled")}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if overrideCalls != 1 {
		t.Fatal("cancelled send reached platform")
	}
}
