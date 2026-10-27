// contracts.go 定义对外公开的请求、结果与业务类型。

package platform

import (
	"context"
	"encoding/json"
	"slices"
)

// ClientResultKind 是 Platform 对连接运行结果的公开分类，App 不解释底层错误。
type ClientResultKind uint8

const (
	ClientCompleted ClientResultKind = iota // 客户端正常结束，Err 为 nil。
	ClientFallback                          // 默认连接不可用，允许进程入口尝试本地运行。
	ClientFailed                            // 其他失败，不允许自动回退。
)

// ClientResult 保留原始失败原因；是否启动本地应用由进程入口决定。
type ClientResult struct {
	Kind ClientResultKind // App 使用的运行结果分类。
	Err  error            // 连接或运行错误；正常结束时为空。
}

// MessageContext 只公开原始消息和引用事实；路由与运行能力留在 Platform 内部。
type MessageContext struct {
	RawText         string          // 平台收到的原始正文，未经 Agent 改写。
	PlatformMessage json.RawMessage // 原生消息载荷，仅作为事实交给 Hook。
	Reply           ReplyContext    // 被引用消息的内容快照。
}

type MessageSegmentType string

const (
	SegmentText  MessageSegmentType = "text"
	SegmentImage MessageSegmentType = "image"
	SegmentFile  MessageSegmentType = "file"
	SegmentAt    MessageSegmentType = "at"
)

// MessageSegment 描述引用内容，不包含下载器、发送器或其他运行能力。
type MessageSegment struct {
	Type           MessageSegmentType `json:"type"`                       // 片段种类。
	Text           string             `json:"text,omitempty"`             // 文本或附件说明。
	UserID         string             `json:"user_id,omitempty"`          // 提及的用户标识。
	URL            string             `json:"url,omitempty"`              // 平台提供的媒体来源地址。
	MediaID        string             `json:"media,omitempty"`            // 已导入媒体中心的标识。
	PlatformFileID string             `json:"platform_file_id,omitempty"` // 平台原生文件标识。
	MIMEType       string             `json:"mime_type,omitempty"`        // 媒体内容类型。
	Name           string             `json:"name,omitempty"`             // 附件展示名称。
	Size           int64              `json:"size,omitempty"`             // 已知的附件字节数。
}

type ReplyContext struct {
	MessageID  string           // 被引用消息的平台标识。
	SenderID   string           // 被引用消息的发送者标识。
	SenderName string           // 发送者展示名称。
	Text       string           // 引用正文。
	Segments   []MessageSegment // 按原始顺序排列的引用片段。
}

type messageContextKey struct{}

// WithMessageContext 保存独立快照，不改变 contextinfo 中的会话事实。
func WithMessageContext(ctx context.Context, msg MessageContext) context.Context {
	msg.PlatformMessage = slices.Clone(msg.PlatformMessage)
	msg.Reply.Segments = slices.Clone(msg.Reply.Segments)
	return context.WithValue(ctx, messageContextKey{}, msg)
}

// MessageContextFrom 返回独立快照，调用方修改不会影响其他消费者。
func MessageContextFrom(ctx context.Context) (MessageContext, bool) {
	msg, ok := ctx.Value(messageContextKey{}).(MessageContext)
	msg.PlatformMessage = slices.Clone(msg.PlatformMessage)
	msg.Reply.Segments = slices.Clone(msg.Reply.Segments)
	return msg, ok
}

func WithoutMessageContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, messageContextKey{}, struct{}{})
}
