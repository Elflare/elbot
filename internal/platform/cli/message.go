// message.go 构造 CLI 消息来源与输入上下文。

package cli

import (
	"context"

	"elbot/internal/contextinfo"
	"elbot/internal/platform/share"
	"elbot/internal/security"
)

// Both scanner and TUI enter through this handler. The public snapshot is per
// message; the private CLI destination is separate from message reply metadata.
type localMessageHandler struct {
	next    share.MessageHandler
	adapter *Adapter
}

func (h localMessageHandler) HandleMessage(ctx context.Context, text string) error {
	info := contextinfo.Conversation{
		PlatformData: messageData{adapter: h.adapter},
		Source:       contextinfo.Source{Platform: h.adapter.Name(), ScopeID: "local", ConversationKind: contextinfo.ConversationCLI, ConversationID: "local"},
		Identity:     contextinfo.Identity{ActorID: security.ActorID(h.adapter.Name(), "local"), PlatformUserID: "local"},
	}
	return h.next(contextinfo.WithConversation(ctx, info), text, share.MessageContext{Conversation: info, GroupRole: contextinfo.GroupRoleUnknown, RawText: text, Sender: h.adapter})
}
