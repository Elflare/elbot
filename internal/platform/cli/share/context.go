package share

import (
	"context"

	"elbot/internal/delivery"
	"elbot/internal/runtime"
)

// Status 是交给原始 CLI 展示目标的运行状态事实。
type Status struct {
	Snapshot runtime.Snapshot
}

// DisplayTarget 表达原始 CLI 会话的回复目的地，不暴露 local/remote 连接对象。
type DisplayTarget interface {
	TargetID() string
	SendReply(context.Context, []delivery.Output) error
	StartStream(context.Context) (delivery.MessageStream, error)
	PresentStatus(context.Context, Status) error
}
