package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/platform"
	"elbot/internal/request"
)

func TestFillHookContextAddsPlatformMessageIDs(t *testing.T) {
	a := &Agent{platform: &fakePlatform{}, scopeID: "default"}
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{
		Platform:          "qq-onebot",
		ScopeID:           "group:123",
		PlatformMessageID: "456",
		ReplyToMessageID:  "789",
		PlatformMessage:   []byte(`[{"type":"json","data":{"data":"{}"}}]`),
	})

	event := a.fillHookContext(ctx, hook.Event{Point: hook.PointPlatformMessageReceived})

	if event.Platform.PlatformMessageID != "456" {
		t.Fatalf("platform message id = %q, want %q", event.Platform.PlatformMessageID, "456")
	}
	if event.Platform.ReplyToMessageID != "789" {
		t.Fatalf("reply to message id = %q, want %q", event.Platform.ReplyToMessageID, "789")
	}
	if got := string(event.Message.PlatformMessage); got != `[{"type":"json","data":{"data":"{}"}}]` {
		t.Fatalf("platform message = %q", got)
	}
	other := a.fillHookContext(ctx, hook.Event{Point: hook.PointAgentInputPrepared})
	if len(other.Message.PlatformMessage) != 0 {
		t.Fatalf("non-platform hook message = %s", other.Message.PlatformMessage)
	}
}

func TestFillHookContextKeepsExplicitPlatformMessageIDs(t *testing.T) {
	a := &Agent{platform: &fakePlatform{}, scopeID: "default"}
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{
		PlatformMessageID: "from-context",
		ReplyToMessageID:  "reply-from-context",
	})

	event := a.fillHookContext(ctx, hook.Event{Platform: hook.PlatformContext{
		PlatformMessageID: "explicit",
		ReplyToMessageID:  "explicit-reply",
	}})

	if event.Platform.PlatformMessageID != "explicit" {
		t.Fatalf("platform message id = %q, want explicit value", event.Platform.PlatformMessageID)
	}
	if event.Platform.ReplyToMessageID != "explicit-reply" {
		t.Fatalf("reply to message id = %q, want explicit value", event.Platform.ReplyToMessageID)
	}
}

func TestFillHookContextAddsIntentTextWithoutWakeupPrefix(t *testing.T) {
	a := &Agent{platform: &fakePlatform{}, scopeID: "default"}
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{
		Platform:         "qq-onebot",
		ScopeID:          "group:123",
		ConversationKind: platform.ConversationGroup,
		TriggerKeywords:  []string{"芙莉丝"},
	})

	event := a.fillHookContext(ctx, hook.Event{
		Point:   hook.PointPlatformMessageReceived,
		Message: hook.MessagePayload{Role: "user", Segments: llm.TextSegments("芙莉丝 咩")},
	})

	if event.Message.IntentText != "咩" {
		t.Fatalf("intent text = %q, want 咩", event.Message.IntentText)
	}
}

func TestRunHookErrorSendsFailureNotice(t *testing.T) {
	p := &fakePlatform{}
	manager := hook.NewManager()
	if err := manager.Register(hook.Registration{
		Point: hook.PointAgentInputPrepared,
		Name:  "test.boom",
		Match: hook.Always(),
		Handler: hook.HandlerFunc(func(context.Context, hook.Event) (hook.Event, error) {
			return hook.Event{}, errors.New("boom")
		}),
	}); err != nil {
		t.Fatalf("register hook: %v", err)
	}
	a := &Agent{platform: p, hooks: manager}
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{
		Platform: "cli",
		ScopeID:  "private:test",
		Sender:   p,
	})

	_, err := a.runHook(ctx, hook.Event{Point: hook.PointAgentInputPrepared})
	if err == nil {
		t.Fatal("expected hook error")
	}
	got := p.out.String()
	for _, want := range []string{"Hook 执行失败", "agent.input.prepared", "hook test.boom", "boom"} {
		if !strings.Contains(got, want) {
			t.Fatalf("notice = %q, want %q", got, want)
		}
	}
}

func TestHookObserverTracksHookRequestUnderTurn(t *testing.T) {
	manager := hook.NewManager()
	a := &Agent{platform: &fakePlatform{}, requests: request.NewManager(time.Minute)}
	a.SetHookManager(manager)
	parent, parentCtx, parentDone, err := a.requests.Start(context.Background(), request.StartRequest{SessionID: "s1", Kind: request.KindTurn, Label: "chat"})
	if err != nil {
		t.Fatalf("start turn: %v", err)
	}
	defer parentDone()
	ctx := withTurnRequestID(parentCtx, parent.ID)

	if err := manager.Register(hook.Registration{
		Point: hook.PointAgentInputPrepared,
		Name:  "test.hook_status",
		Match: hook.Always(),
		Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
			var hookReq request.Request
			for _, req := range a.requests.List() {
				if req.Kind == request.KindHook {
					hookReq = req
					break
				}
			}
			if hookReq.ID == "" {
				t.Fatal("hook request was not active while handler ran")
			}
			if hookReq.ParentID != parent.ID || hookReq.SessionID != "s1" || hookReq.Label != "test.hook_status" {
				t.Fatalf("hook request = %#v", hookReq)
			}
			return event, nil
		}),
	}); err != nil {
		t.Fatalf("register hook: %v", err)
	}

	if _, err := manager.Run(ctx, hook.Event{Point: hook.PointAgentInputPrepared, Session: hook.SessionContext{ID: "s1"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, req := range a.requests.List() {
		if req.Kind == request.KindHook {
			t.Fatalf("hook request leaked after handler finished: %#v", req)
		}
	}
}

func TestStopCanCancelTrackedHookRequest(t *testing.T) {
	manager := hook.NewManager()
	a := &Agent{platform: &fakePlatform{}, requests: request.NewManager(time.Minute)}
	a.SetHookManager(manager)
	entered := make(chan struct{})
	if err := manager.Register(hook.Registration{
		Point: hook.PointAgentInputPrepared,
		Name:  "test.cancel_hook",
		Match: hook.Always(),
		Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
			close(entered)
			<-ctx.Done()
			return event, ctx.Err()
		}),
	}); err != nil {
		t.Fatalf("register hook: %v", err)
	}

	errCh := make(chan error, 1)
	go func() {
		_, err := manager.Run(context.Background(), hook.Event{Point: hook.PointAgentInputPrepared, Session: hook.SessionContext{ID: "s1"}})
		errCh <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("hook handler did not start")
	}
	var hookReq request.Request
	for _, req := range a.requests.List() {
		if req.Kind == request.KindHook {
			hookReq = req
			break
		}
	}
	if hookReq.ID == "" {
		t.Fatal("hook request not found")
	}
	if !a.requests.Cancel(hookReq.ID) {
		t.Fatal("Cancel returned false for hook request")
	}
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("hook run did not stop after cancel")
	}
}
