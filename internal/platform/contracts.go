// contracts.go 定义对外公开的请求、结果与业务类型。

package platform

import (
	"context"
	"encoding/json"
	"slices"

	"elbot/internal/platform/share"
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

type MessageSegmentType = share.MessageSegmentType

const (
	SegmentText  = share.SegmentText
	SegmentImage = share.SegmentImage
	SegmentFile  = share.SegmentFile
	SegmentAt    = share.SegmentAt
)

// MessageSegment 描述引用内容，不包含下载器、发送器或其他运行能力。
type MessageSegment = share.MessageSegment

type ReplyContext = share.ReplyContext

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
