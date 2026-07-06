package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"elbot/internal/hook"
	"elbot/internal/platform"
)

func TestFillHookContextAddsPlatformMessageIDs(t *testing.T) {
	a := &Agent{platform: &fakePlatform{}, scopeID: "default"}
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{
		Platform:          "qq-onebot",
		ScopeID:           "group:123",
		PlatformMessageID: "456",
		ReplyToMessageID:  "789",
	})

	event := a.fillHookContext(ctx, hook.Event{})

	if event.Platform.PlatformMessageID != "456" {
		t.Fatalf("platform message id = %q, want %q", event.Platform.PlatformMessageID, "456")
	}
	if event.Platform.ReplyToMessageID != "789" {
		t.Fatalf("reply to message id = %q, want %q", event.Platform.ReplyToMessageID, "789")
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
