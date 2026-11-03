// message.go 构造 CLI 消息来源与输入上下文。

package cli

import (
	"context"

	"elbot/internal/contextinfo"
	"elbot/internal/platform/share"
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
		Source:       contextinfo.Source{Platform: h.adapter.Name(), ChatType: contextinfo.ChatPrivate, ChatID: "local", Name: h.adapter.userName},
		Identity:     contextinfo.Identity{ID: "local", Name: h.adapter.userName},
	}
	return h.next(contextinfo.WithConversation(ctx, info), text, share.MessageContext{Conversation: info, GroupRole: contextinfo.GroupRoleUnknown, RawText: text, Sender: h.adapter})
}
