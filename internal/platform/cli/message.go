// message.go 构造 CLI 消息来源与输入上下文。

package cli

import (
	"context"

	"elbot/internal/contextinfo"
	"elbot/internal/platform"
)

// Both scanner and TUI enter through this handler. The public snapshot is per
// message; the private CLI destination is separate from message reply metadata.
type localMessageHandler struct {
	next    platform.PlatformHandler
	adapter *Adapter
}

func (h localMessageHandler) HandleMessage(ctx context.Context, text string) error {
	info := contextinfo.Conversation{
		PlatformData: messageData{adapter: h.adapter},
		Source:       contextinfo.Source{Platform: "cli", ScopeID: "local", ConversationKind: contextinfo.ConversationUnknown, ConversationID: "local"},
		Identity:     contextinfo.Identity{ActorID: "cli:local", PlatformUserID: "local"},
	}
	return h.next.HandleMessage(contextinfo.WithConversation(ctx, info), text)
}
