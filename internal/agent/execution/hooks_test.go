package execution

import (
	"context"
	"errors"

	"testing"
	"time"

	agenthooks "elbot/internal/agent/hooks"
	agentshare "elbot/internal/agent/share"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/platform"
	"elbot/internal/request"
)

func TestFillHookContextAddsPlatformMessageIDs(t *testing.T) {
	bridge := agenthooks.NewHookBridge(agenthooks.HookBridgeDeps{Identity: agentshare.NewIdentityResolver(agentshare.IdentityResolverDeps{ScopeID: "default"})})
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{Conversation: contextinfo.Conversation{Source: contextinfo.Source{Platform: "qq-onebot",
		ScopeID: "group:123"}, PlatformMessageID: "456",
		ReplyToMessageID: "789"},
		PlatformMessage: []byte(`[{"type":"json","data":{"data":"{}"}}]`)})

	event := bridge.FillContext(ctx, hook.Event{Point: hook.PointPlatformMessageReceived})

	if event.Platform.PlatformMessageID != "456" {
		t.Fatalf("platform message id = %q, want %q", event.Platform.PlatformMessageID, "456")
	}
	if event.Platform.ReplyToMessageID != "789" {
		t.Fatalf("reply to message id = %q, want %q", event.Platform.ReplyToMessageID, "789")
	}
	if got := string(event.Message.PlatformMessage); got != `[{"type":"json","data":{"data":"{}"}}]` {
		t.Fatalf("platform message = %q", got)
	}
	other := bridge.FillContext(ctx, hook.Event{Point: hook.PointAgentInputPrepared})
	if len(other.Message.PlatformMessage) != 0 {
		t.Fatalf("non-platform hook message = %s", other.Message.PlatformMessage)
	}
}

func TestFillHookContextKeepsExplicitPlatformMessageIDs(t *testing.T) {
	bridge := agenthooks.NewHookBridge(agenthooks.HookBridgeDeps{Identity: agentshare.NewIdentityResolver(agentshare.IdentityResolverDeps{ScopeID: "default"})})
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{Conversation: contextinfo.Conversation{
		PlatformMessageID: "from-context",
		ReplyToMessageID:  "reply-from-context"}})

	event := bridge.FillContext(ctx, hook.Event{Platform: hook.PlatformContext{
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
	bridge := agenthooks.NewHookBridge(agenthooks.HookBridgeDeps{Identity: agentshare.NewIdentityResolver(agentshare.IdentityResolverDeps{ScopeID: "default"})})
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{Conversation: contextinfo.Conversation{Source: contextinfo.Source{Platform: "qq-onebot",
		ScopeID:          "group:123",
		ConversationKind: contextinfo.ConversationGroup}}, TriggerKeywords: []string{"芙莉丝"},
	})

	event := bridge.FillContext(ctx, hook.Event{
		Point:   hook.PointPlatformMessageReceived,
		Message: hook.MessagePayload{Role: "user", Segments: llm.TextSegments("芙莉丝 咩")},
	})

	if event.Message.IntentText != "咩" {
		t.Fatalf("intent text = %q, want 咩", event.Message.IntentText)
	}
}

func TestHookObserverTracksHookRequestUnderTurn(t *testing.T) {
	manager := newTestHookManager()

	requests := request.NewManager(time.Minute)
	bridge := agenthooks.NewHookBridge(agenthooks.HookBridgeDeps{Requests: requests})
	manager.SetObserver(bridge.ObserveRun)
	parent, parentCtx, parentDone := requests.Start(context.Background(), request.StartRequest{SessionID: "s1", Kind: request.KindTurn, Label: "chat"})
	defer parentDone()
	ctx := parentCtx

	manager.Register(hook.Registration{
		Point: hook.PointAgentInputPrepared,
		Name:  "test.hook_status",
		Match: hook.Always(),
		Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, events.LogResult, error) {
			var hookReq request.Request
			for _, req := range requests.List() {
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
			return event, events.LogResult{ResultStatus: events.ResultSucceeded, Content: "test source completed"}, nil
		}),
	})

	if _, _, err := manager.Run(ctx, hook.Event{Point: hook.PointAgentInputPrepared, Session: hook.SessionContext{ID: "s1"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, req := range requests.List() {
		if req.Kind == request.KindHook {
			t.Fatalf("hook request leaked after handler finished: %#v", req)
		}
	}
}

func TestStopCanCancelTrackedHookRequest(t *testing.T) {
	manager := newTestHookManager()

	requests := request.NewManager(time.Minute)
	bridge := agenthooks.NewHookBridge(agenthooks.HookBridgeDeps{Requests: requests})
	manager.SetObserver(bridge.ObserveRun)
	entered := make(chan struct{})
	manager.Register(hook.Registration{
		Point: hook.PointAgentInputPrepared,
		Name:  "test.cancel_hook",
		Match: hook.Always(),
		Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, events.LogResult, error) {
			close(entered)
			<-ctx.Done()
			return event, events.LogResult{ResultStatus: events.ResultCanceled, Content: "test source canceled"}, ctx.Err()
		}),
	})

	errCh := make(chan error, 1)
	go func() {
		_, _, err := manager.Run(context.Background(), hook.Event{Point: hook.PointAgentInputPrepared, Session: hook.SessionContext{ID: "s1"}})
		errCh <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("hook handler did not start")
	}
	var hookReq request.Request
	for _, req := range requests.List() {
		if req.Kind == request.KindHook {
			hookReq = req
			break
		}
	}
	if hookReq.ID == "" {
		t.Fatal("hook request not found")
	}
	if !requests.Cancel(hookReq.ID) {
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
