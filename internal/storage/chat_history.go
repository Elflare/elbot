package storage

import (
	"encoding/json"
	"net/url"
	"path"
	"strings"
)

// ChatSegment 是历史记录的有序片段，不携带协议对象或运行能力。
type ChatSegment struct {
	Type           string `json:"type"`                       // 片段种类：text、image、file 或 at。
	Text           string `json:"text,omitempty"`             // 正文或附件说明。
	UserID         string `json:"user_id,omitempty"`          // 被提及的用户标识。
	URL            string `json:"url,omitempty"`              // 可持久保存的媒体来源地址。
	MediaID        string `json:"media,omitempty"`            // 已导入媒体的标识；历史编码时清除。
	PlatformFileID string `json:"platform_file_id,omitempty"` // 平台原生文件标识。
	MIMEType       string `json:"mime_type,omitempty"`        // 已知内容类型。
	Name           string `json:"name,omitempty"`             // 附件展示名。
	Size           int64  `json:"size,omitempty"`             // 已知字节数。
}

// ChatReply 是历史引用的内容快照，消息 ID 单独保存在历史行中。
type ChatReply struct {
	MessageID  string // 被引用的平台消息标识。
	SenderID   string // 被引用消息的发送者标识。
	SenderName string // 被引用消息的发送者展示名。
	Text       string // 被引用消息的正文。
}

type chatMetadata struct {
	Reply *chatReplyMetadata `json:"reply,omitempty"` // 可选的引用内容快照。
}

type chatReplyMetadata struct {
	SenderID   string `json:"sender_id,omitempty"`   // 引用发送者的平台标识。
	SenderName string `json:"sender_name,omitempty"` // 引用发送者的展示名。
	Text       string `json:"text,omitempty"`        // 引用正文快照。
}

func MarshalChatMetadata(reply ChatReply) string {
	if strings.TrimSpace(reply.MessageID) == "" {
		return ""
	}
	data, _ := json.Marshal(chatMetadata{Reply: &chatReplyMetadata{
		SenderID: strings.TrimSpace(reply.SenderID), SenderName: strings.TrimSpace(reply.SenderName), Text: strings.TrimSpace(reply.Text),
	}})
	return string(data)
}

func ChatMessageReply(message ChatMessage) ChatReply {
	reply := ChatReply{MessageID: strings.TrimSpace(message.ReplyToPlatformMessageID)}
	var metadata chatMetadata
	if json.Unmarshal([]byte(strings.TrimSpace(message.Metadata)), &metadata) == nil && metadata.Reply != nil {
		reply.SenderID, reply.SenderName, reply.Text = strings.TrimSpace(metadata.Reply.SenderID), strings.TrimSpace(metadata.Reply.SenderName), strings.TrimSpace(metadata.Reply.Text)
	}
	return reply
}

// MarshalChatSegments 清除临时、敏感来源和物化结果后保存片段，保留原始顺序。
func MarshalChatSegments(segments []ChatSegment) string {
	stored := make([]ChatSegment, 0, len(segments))
	for _, segment := range segments {
		if segment.Type != "text" && segment.Type != "image" && segment.Type != "file" {
			continue
		}
		segment.MediaID = ""
		segment.URL = chatMediaURL(segment.URL)
		if segment.Type != "text" {
			if strings.ContainsAny(segment.PlatformFileID, "/\\") || strings.Contains(segment.PlatformFileID, ":") {
				segment.PlatformFileID = ""
			}
			if strings.Contains(segment.Name, "://") || strings.HasPrefix(segment.Name, "data:") || strings.HasPrefix(segment.Name, "base64:") {
				segment.Name = ""
			} else {
				segment.Name = path.Base(strings.ReplaceAll(segment.Name, "\\", "/"))
				if segment.Name == "." {
					segment.Name = ""
				}
			}
		}
		stored = append(stored, segment)
	}
	if len(stored) == 0 {
		return ""
	}
	data, _ := json.Marshal(stored)
	return string(data)
}

func UnmarshalChatSegments(value string) []ChatSegment {
	var segments []ChatSegment
	if json.Unmarshal([]byte(strings.TrimSpace(value)), &segments) != nil {
		return nil
	}
	return segments
}

func chatMediaURL(value string) string {
	u, err := url.Parse(strings.TrimSpace(value))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return ""
	}
	if strings.Contains(strings.ToLower(u.Path), "/bot") {
		return ""
	}
	for key := range u.Query() {
		key = strings.ToLower(key)
		if strings.HasPrefix(key, "x-amz-") || strings.HasPrefix(key, "x-goog-") || strings.Contains(key, "token") || strings.Contains(key, "signature") || key == "sig" || key == "rkey" || key == "auth_key" {
			return ""
		}
	}
	return u.String()
}
