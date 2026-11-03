package cli

import (
	"context"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/platform/share"
)

type captureLocalInfo struct {
	infos []contextinfo.Conversation
	roles []contextinfo.GroupRole
}

func (h *captureLocalInfo) HandleMessage(ctx context.Context, _ string, msg share.MessageContext) error {
	info, _ := contextinfo.ConversationFromContext(ctx)
	h.infos = append(h.infos, info)
	h.roles = append(h.roles, msg.GroupRole)
	return nil
}

func TestLocalInputProvidesIdentity(t *testing.T) {
	h := &captureLocalInfo{}
	entry := localMessageHandler{next: h.HandleMessage, adapter: New(testPlatformName)}
	ctx := context.Background()
	if err := entry.HandleMessage(ctx, "first"); err != nil {
		t.Fatal(err)
	}
	if err := entry.HandleMessage(ctx, "second"); err != nil {
		t.Fatal(err)
	}
	for i, info := range h.infos {
		if info.Source.Platform != "cli" || info.Source.ScopeID != "local" || info.Source.ConversationKind != contextinfo.ConversationCLI || info.Source.ConversationID != "local" || info.Identity.ActorID != "cli:local" || info.Identity.PlatformUserID != "local" {
			t.Fatal(info)
		}
		if h.roles[i] != contextinfo.GroupRoleUnknown {
			t.Fatalf("group role = %q, want unknown", h.roles[i])
		}
	}
}
