// reference.go 定义适配器和引用恢复组件协作的输入、结果与能力契约。

package share

import (
	"elbot/internal/storage"
	"encoding/json"
	"strings"
)

type chatMetadata struct {
	Reply *chatReplyMetadata `json:"reply,omitempty"` // 可选的引用内容快照。
}
type chatReplyMetadata struct {
	SenderID   string `json:"sender_id,omitempty"`   // 被引用消息的发送者 ID。
	SenderName string `json:"sender_name,omitempty"` // 被引用消息的展示名。
	Text       string `json:"text,omitempty"`        // 被引用消息的正文。
}

func MarshalChatMetadata(reply ReplyContext) string {
	if strings.TrimSpace(reply.MessageID) == "" {
		return ""
	}
	data, err := json.Marshal(chatMetadata{Reply: &chatReplyMetadata{
		SenderID: strings.TrimSpace(reply.SenderID), SenderName: strings.TrimSpace(reply.SenderName), Text: strings.TrimSpace(reply.Text),
	}})
	if err != nil {
		return ""
	}
	return string(data)
}

func ChatMessageReply(message storage.ChatMessage) ReplyContext {
	reply := ReplyContext{MessageID: strings.TrimSpace(message.ReplyToPlatformMessageID)}
	var metadata chatMetadata
	if json.Unmarshal([]byte(strings.TrimSpace(message.Metadata)), &metadata) == nil && metadata.Reply != nil {
		reply.SenderID, reply.SenderName, reply.Text = strings.TrimSpace(metadata.Reply.SenderID), strings.TrimSpace(metadata.Reply.SenderName), strings.TrimSpace(metadata.Reply.Text)
	}
	return reply
}
