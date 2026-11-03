// router_test.go 验证并发发送的来源快照隔离、显式目标优先及部分发送回执保留。

package dispatch

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/platform/share"
)

type testSender struct {
	send func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, error)
}

func (s testSender) Name() string { return "primary" }
func (s testSender) SendTo(ctx context.Context, noticeTarget delivery.Target, noticeOutputs []delivery.Output) (delivery.Receipt, error) {
	return s.send(ctx, noticeTarget, noticeOutputs)
}

func TestRouterPreservesConcurrentSourceSnapshots(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]contextinfo.Conversation{}
	sender := testSender{send: func(ctx context.Context, noticeTarget delivery.Target, noticeOutputs []delivery.Output) (delivery.Receipt, error) {
		info, ok := contextinfo.ConversationFromContext(ctx)
		if !ok {
			{
				err := errors.New("missing source")
				return delivery.Receipt{}, err
			}
		}
		mu.Lock()
		seen[noticeOutputs[0].Text] = info
		mu.Unlock()
		return delivery.Receipt{}, nil
	}}
	router := New(Options{Primary: sender})
	var wg sync.WaitGroup
	for i := range 24 {
		id := fmt.Sprint(i)
		info := contextinfo.Conversation{Source: contextinfo.Source{Platform: sender.Name(), ChatID: id}, PlatformMessageID: "message-" + id, PlatformData: id}
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := contextinfo.WithConversation(context.Background(), info)
			if _, err := router.SendTo(ctx, delivery.Target{}, []delivery.Output{delivery.Text(id)}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(seen) != 24 {
		t.Fatalf("received %d snapshots", len(seen))
	}
	for id, info := range seen {
		if info.Source.ChatID != id || info.PlatformMessageID != "message-"+id || info.PlatformData != id {
			t.Fatalf("mixed source: %#v", info)
		}
	}
}

func TestRouterExplicitTargetOverridesSenderAndKeepsPartialReceipt(t *testing.T) {
	wantErr := errors.New("second output failed")
	primaryCalls, overrideCalls := 0, 0
	primary := testSender{send: func(_ context.Context, nTarget delivery.Target, nOutputs []delivery.Output) (delivery.Receipt, error) {
		primaryCalls++
		if nTarget.ChatID != "other" {
			t.Fatalf("target lost: %#v", nTarget)
		}
		return delivery.Receipt{PlatformMessageIDs: []string{"sent"}}, wantErr
	}}
	override := testSender{send: func(_ context.Context, nTarget delivery.Target, nOutputs []delivery.Output) (delivery.Receipt, error) {
		overrideCalls++
		if !nTarget.Empty() {
			t.Fatal("default target became explicit")
		}
		return delivery.Receipt{}, nil
	}}
	router := New(Options{Primary: primary})
	ctx := share.WithMessageContext(context.Background(), share.MessageContext{Sender: override})
	if _, err := router.SendTo(ctx, delivery.Target{}, []delivery.Output{delivery.Text("reply")}); err != nil {
		t.Fatal(err)
	}
	got, err := router.SendTo(ctx, delivery.Target{ChatType: "private", ChatID: "other"}, []delivery.Output{delivery.Text("explicit")})
	if !errors.Is(err, wantErr) || len(got.PlatformMessageIDs) != 1 || primaryCalls != 1 || overrideCalls != 1 {
		t.Fatalf("receipt=%#v error=%v calls=%d/%d", got, err, primaryCalls, overrideCalls)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := router.SendTo(cancelled, delivery.Target{}, []delivery.Output{delivery.Text("cancelled")}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if overrideCalls != 1 {
		t.Fatal("cancelled send reached platform")
	}
}
