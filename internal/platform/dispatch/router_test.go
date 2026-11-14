// router_test.go 验证并发发送的来源快照隔离、显式目标优先及发送失败回执丢弃。

package dispatch

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/operationresult"
	"elbot/internal/platform/share"
	"elbot/internal/signal"
)

type testSender struct {
	send func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, error)
}

type structuredStreamSender struct {
	testSender
	events chan delivery.StreamEvent
}

func (s structuredStreamSender) SupportsToolCallEvents() bool { return true }
func (s structuredStreamSender) StartStream(context.Context) (delivery.MessageStream, error) {
	return delivery.StreamFunc(func(_ context.Context, event delivery.StreamEvent) (delivery.Receipt, error) {
		s.events <- event
		return delivery.Receipt{}, nil
	}), nil
}

func (s testSender) Name() string { return "primary" }
func (s testSender) SendTo(ctx context.Context, noticeTarget delivery.Target, noticeOutputs []delivery.Output) (delivery.Receipt, error) {
	return s.send(ctx, noticeTarget, noticeOutputs)
}

func TestRouterStreamFallbackPreservesBlocksAndActions(t *testing.T) {
	got := make(chan delivery.Output, 1)
	sender := testSender{send: func(_ context.Context, _ delivery.Target, outputs []delivery.Output) (delivery.Receipt, error) {
		got <- outputs[0]
		return delivery.Receipt{}, nil
	}}
	router := New(Options{Primary: sender})
	ctx := contextinfo.WithConversation(t.Context(), contextinfo.Conversation{Source: contextinfo.Source{Platform: sender.Name(), ChatID: "chat"}})
	stream, err := router.StartStream(ctx)
	if err != nil || stream.Status != operationresult.Success {
		t.Fatalf("stream=%+v err=%v", stream, err)
	}
	blocks := []delivery.ContentBlock{{Kind: delivery.ContentText, Content: "确认预览"}, {Kind: delivery.ContentDiff, Content: "-before\n+after"}}
	actions := []delivery.Action{{ID: "confirm", Label: "确认", Command: "/confirm"}}
	if _, err := stream.Data.Send(ctx, delivery.StreamEvent{Type: delivery.StreamSend, Blocks: blocks, Actions: actions}); err != nil {
		t.Fatal(err)
	}
	output := <-got
	if len(output.Blocks) != len(blocks) || output.Blocks[1].Kind != delivery.ContentDiff || output.Blocks[1].Content != "-before\n+after" || len(output.Actions) != 1 || output.Actions[0] != actions[0] {
		t.Fatalf("fallback output lost structured content: %#v", output)
	}
}

func TestRouterNotifiesOnlyPrivateCallsWithoutAssistantText(t *testing.T) {
	notifications := make(chan events.Notification, 16)
	connection, err := events.NotificationRequested.Observe(func(_ context.Context, notice events.Notification) {
		notifications <- notice
	}, signal.ConnectOptions{Shutdown: signal.Drain})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()

	sender := testSender{send: func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, error) {
		t.Fatal("tool notice bypassed notification routing")
		return delivery.Receipt{}, nil
	}}
	router := New(Options{Primary: sender})
	calls := []llm.ToolCallRequest{
		{ID: "call-1", Name: "shell", Arguments: `{"cmd":"pwd"}`},
		{ID: "call-2", Name: "read_file", Arguments: `{"path":"one"}`},
		{ID: "call-3", Name: "read_file", Arguments: `{"path":"two"}`},
	}
	for _, tc := range []struct {
		name     string
		chatType contextinfo.ChatType
		speaking bool
		count    int
		want     string
	}{
		{name: "group", chatType: contextinfo.ChatGroup, count: 3},
		{name: "assistant already speaking", chatType: contextinfo.ChatPrivate, speaking: true, count: 3},
		{name: "private", chatType: contextinfo.ChatPrivate, count: 1, want: `[tool] 正在调用 shell：{"cmd":"pwd"}`},
		{name: "private batch", chatType: contextinfo.ChatPrivate, count: 3, want: `[tool] 正在调用 shell：{"cmd":"pwd"}
[tool] 正在调用 read_file：{"path":"one"}
[tool] 正在调用 read_file：{"path":"two"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := contextinfo.WithConversation(t.Context(), contextinfo.Conversation{Source: contextinfo.Source{
				Platform: sender.Name(), ChatType: tc.chatType, ChatID: "chat",
			}})
			stream, err := router.StartStream(ctx)
			if err != nil || stream.Status != operationresult.Success {
				t.Fatalf("stream=%+v err=%v", stream, err)
			}
			// 两个连续批次各发一条，不因使用同一流而互相合并。
			for range 2 {
				_, err = stream.Data.Send(ctx, delivery.StreamEvent{
					Type: delivery.StreamToolCalls, ToolCalls: calls[:tc.count], AssistantSpeaking: tc.speaking,
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := events.NotificationRequested.Emit(ctx, events.Notification{Text: "barrier"}); err != nil {
				t.Fatal(err)
			}
			count := 0
			for len(notifications) > 0 {
				got := <-notifications
				if got.Text == "barrier" {
					continue
				}
				count++
				if tc.want == "" || got.Text != tc.want || got.Origin == nil || got.Origin.Source.ChatType != contextinfo.ChatPrivate {
					t.Fatalf("notification = %+v", got)
				}
			}
			want := 0
			if tc.want != "" {
				want = 2
			}
			if count != want {
				t.Fatalf("notifications = %d, want %d", count, want)
			}
		})
	}
}

func TestRouterForwardsStructuredToolCallsForGroupConversation(t *testing.T) {
	calls := []llm.ToolCallRequest{
		{ID: "call-1", Name: "shell", Arguments: ` { "cmd": "pwd" } `},
		{ID: "call-2", Name: "read_file", Arguments: `{"path":"one"}`},
		{ID: "call-3", Name: "read_file", Arguments: `{"path":"two"}`},
	}
	sender := structuredStreamSender{testSender: testSender{}, events: make(chan delivery.StreamEvent, len(calls))}
	router := New(Options{Primary: sender})
	ctx := contextinfo.WithConversation(t.Context(), contextinfo.Conversation{Source: contextinfo.Source{
		Platform: sender.Name(), ChatType: contextinfo.ChatGroup, ChatID: "group",
	}})
	stream, err := router.StartStream(ctx)
	if err != nil || stream.Status != operationresult.Success {
		t.Fatalf("stream=%+v err=%v", stream, err)
	}
	if _, err := stream.Data.Send(ctx, delivery.StreamEvent{Type: delivery.StreamToolCalls, ToolCalls: calls, AssistantSpeaking: true}); err != nil {
		t.Fatal(err)
	}
	if len(sender.events) != len(calls) {
		t.Fatalf("structured events = %d, want %d", len(sender.events), len(calls))
	}
	for _, call := range calls {
		got := <-sender.events
		if got.Type != delivery.StreamToolCall || got.ToolCallID != call.ID || got.ToolCall == nil || *got.ToolCall != (delivery.ToolCall{Name: call.Name, Arguments: call.Arguments}) || len(got.ToolCalls) != 0 {
			t.Fatalf("structured tool call = %+v, want %+v", got, call)
		}
	}
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
			if result, err := router.SendTo(ctx, delivery.Target{}, []delivery.Output{delivery.Text(id)}); err != nil || result.Status != operationresult.Success {
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

func TestRouterExplicitTargetOverridesSenderAndDiscardsPartialReceipt(t *testing.T) {
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
	if result, err := router.SendTo(ctx, delivery.Target{}, []delivery.Output{delivery.Text("reply")}); err != nil || result.Status != operationresult.Success {
		t.Fatalf("default send result=%+v err=%v", result, err)
	}
	got, err := router.SendTo(ctx, delivery.Target{ChatType: "private", ChatID: "other"}, []delivery.Output{delivery.Text("explicit")})
	if !errors.Is(err, wantErr) || got.Status != "" || len(got.Data.PlatformMessageIDs) != 0 || primaryCalls != 1 || overrideCalls != 1 {
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
