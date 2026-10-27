// message.go 定义内部消息、片段、引用与消息上下文。

package share

import (
	"context"
	"elbot/internal/delivery"
)

type MessageSegmentType string

const (
	SegmentText  MessageSegmentType = "text"
	SegmentImage MessageSegmentType = "image"
	SegmentFile  MessageSegmentType = "file"
	SegmentAt    MessageSegmentType = "at"
)

// MessageSegment 是平台内部处理和存储的有序消息片段。
type MessageSegment struct {
	Type           MessageSegmentType `json:"type"`                       // 片段种类。
	Text           string             `json:"text,omitempty"`             // 文本内容。
	UserID         string             `json:"user_id,omitempty"`          // 提及用户标识。
	URL            string             `json:"url,omitempty"`              // 媒体来源地址。
	MediaID        string             `json:"media,omitempty"`            // 已导入的媒体 ID。
	PlatformFileID string             `json:"platform_file_id,omitempty"` // 平台文件标识。
	MIMEType       string             `json:"mime_type,omitempty"`        // 内容类型。
	Name           string             `json:"name,omitempty"`             // 文件名。
	Size           int64              `json:"size,omitempty"`             // 已知字节数。
}

type ReplyContext struct {
	MessageID  string           // 被引用的平台消息 ID。
	SenderID   string           // 被引用消息的发送者 ID。
	SenderName string           // 发送者展示名。
	Text       string           // 引用正文。
	Segments   []MessageSegment // 引用消息片段。
}

// MediaResolver 仅表达协议侧媒体来源解析，不负责媒体缓存。
type MediaResolver interface {
	ResolveMedia(context.Context, MessageSegment, int64) (delivery.Source, error)
}
