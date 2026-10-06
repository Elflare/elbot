package cli

import (
	"context"

	"elbot/internal/contextinfo"
	"elbot/internal/platform"
)

// Both scanner and TUI enter through this handler. The public snapshot is per
// message; no platform reply metadata is fabricated for a local terminal.
type localMessageHandler struct{ next platform.PlatformHandler }

func (h localMessageHandler) HandleMessage(ctx context.Context, text string) error {
	info := contextinfo.Conversation{
		Source:   contextinfo.Source{Platform: "cli", ScopeID: "local", ConversationKind: contextinfo.ConversationUnknown, ConversationID: "local"},
		Identity: contextinfo.Identity{ActorID: "cli:local", PlatformUserID: "local"},
	}
	return h.next.HandleMessage(contextinfo.WithConversation(ctx, info), text)
}
