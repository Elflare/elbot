// router_test.go 验证随组件迁入的既有行为。

package dispatch

import (
	"context"
	"elbot/internal/events"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/platform/share"
)

type testSender struct {
	send func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, events.LogResult, error)
}

func (s testSender) Name() string { return "primary" }
func (s testSender) SendChat(ctx context.Context, outputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
	return s.send(ctx, delivery.Target{}, outputs)
}
func (s testSender) SendTo(ctx context.Context, noticeTarget delivery.Target, noticeOutputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
	return s.send(ctx, noticeTarget, noticeOutputs)
}

func TestRouterPreservesConcurrentSourceSnapshots(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]contextinfo.Conversation{}
	sender := testSender{send: func(ctx context.Context, noticeTarget delivery.Target, noticeOutputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
		info, ok := contextinfo.ConversationFromContext(ctx)
		if !ok {
			{
				err := errors.New("missing source")
				logResult := events.LogResult{ResultStatus: events.ResultSucceeded}
				if err != nil {
					logResult.ResultStatus = events.ResultFailed
					logResult.Content = err.Error()
					if err == context.Canceled {
						logResult.ResultStatus = events.ResultCanceled
					}
				}
				return delivery.Receipt{}, logResult, err
			}
		}
		mu.Lock()
		seen[noticeOutputs[0].Text] = info
		mu.Unlock()
		return delivery.Receipt{}, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
	}}
	router := New(Options{Primary: sender})
	var wg sync.WaitGroup
	for i := range 24 {
		id := fmt.Sprint(i)
		info := contextinfo.Conversation{Source: contextinfo.Source{Platform: sender.Name(), ScopeID: id}, PlatformMessageID: "message-" + id, PlatformData: id}
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := contextinfo.WithConversation(context.Background(), info)
			if _, _, err := router.SendTo(ctx, delivery.Target{}, []delivery.Output{delivery.Text(id)}); err != nil {
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
	primary := testSender{send: func(_ context.Context, nTarget delivery.Target, nOutputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
		primaryCalls++
		if nTarget.PrivateUserID != "other" {
			t.Fatalf("target lost: %#v", nTarget)
		}
		{
			logResult := events.LogResult{ResultStatus: events.ResultSucceeded}
			if wantErr != nil {
				logResult.ResultStatus = events.ResultFailed
				logResult.Content = wantErr.Error()
				if wantErr == context.Canceled {
					logResult.ResultStatus = events.ResultCanceled
				}
			}
			return delivery.Receipt{PlatformMessageIDs: []string{"sent"}}, logResult, wantErr
		}
	}}
	override := testSender{send: func(_ context.Context, nTarget delivery.Target, nOutputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
		overrideCalls++
		if !nTarget.Empty() {
			t.Fatal("default target became explicit")
		}
		return delivery.Receipt{}, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
	}}
	router := New(Options{Primary: primary})
	ctx := share.WithMessageContext(context.Background(), share.MessageContext{Sender: override})
	if _, _, err := router.SendTo(ctx, delivery.Target{}, []delivery.Output{delivery.Text("reply")}); err != nil {
		t.Fatal(err)
	}
	got, logResult, err := router.SendTo(ctx, delivery.Target{PrivateUserID: "other"}, []delivery.Output{delivery.Text("explicit")})
	if !errors.Is(err, wantErr) || logResult.ResultStatus != events.ResultFailed || !strings.Contains(logResult.Content, wantErr.Error()) || len(got.PlatformMessageIDs) != 1 || primaryCalls != 1 || overrideCalls != 1 {
		t.Fatalf("receipt=%#v error=%v calls=%d/%d", got, err, primaryCalls, overrideCalls)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := router.SendChat(cancelled, []delivery.Output{delivery.Text("cancelled")}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if overrideCalls != 1 {
		t.Fatal("cancelled send reached platform")
	}
}
