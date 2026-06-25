package agent

import (
	"context"
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
