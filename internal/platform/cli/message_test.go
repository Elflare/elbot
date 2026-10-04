package cli

import (
	"context"
	"testing"

	"elbot/internal/chatinfo"
	"elbot/internal/platform"
)

type captureLocalInfo struct {
	infos           []chatinfo.Info
	hasReplyContext bool
}

func (h *captureLocalInfo) HandleMessage(ctx context.Context, _ string) error {
	info, _ := chatinfo.FromContext(ctx)
	h.infos = append(h.infos, info)
	_, h.hasReplyContext = platform.MessageContextFrom(ctx)
	return nil
}

func TestLocalInputProvidesIdentityWithoutPlatformReplyContext(t *testing.T) {
	h := &captureLocalInfo{}
	entry := localMessageHandler{next: h}
	ctx := context.Background()
	if err := entry.HandleMessage(ctx, "first"); err != nil {
		t.Fatal(err)
	}
	if err := entry.HandleMessage(ctx, "second"); err != nil {
		t.Fatal(err)
	}
	for _, info := range h.infos {
		if info.Source.Platform != "cli" || info.Source.ScopeID != "local" || info.Identity.ActorID != "cli:local" || info.Identity.PlatformUserID != "local" {
			t.Fatal(info)
		}
	}
	if h.hasReplyContext {
		t.Fatal("unexpected platform reply context")
	}
}
