package cli

import (
	"context"

	"elbot/internal/chatinfo"
	"elbot/internal/platform"
)

// Both scanner and TUI enter through this handler. The public snapshot is per
// message; no platform reply metadata is fabricated for a local terminal.
type localMessageHandler struct{ next platform.PlatformHandler }

func (h localMessageHandler) HandleMessage(ctx context.Context, text string) error {
	info := chatinfo.Info{
		Source:   chatinfo.Source{Platform: "cli", ScopeID: "local", ConversationKind: chatinfo.ConversationUnknown, ConversationID: "local"},
		Identity: chatinfo.Identity{ActorID: "cli:local", PlatformUserID: "local"},
	}
	return h.next.HandleMessage(chatinfo.WithInfo(ctx, info), text)
}
