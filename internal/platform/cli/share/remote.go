package share

import (
	"context"
	"io"

	"elbot/internal/completion"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/media"
	platformshare "elbot/internal/platform/share"
)

// Runtime 是 local/remote 实现向 CLI 根包提供的平台运行能力。
type Runtime interface {
	platformshare.Runtime
	NoticePresenter
	SetCompleter(func(context.Context, completion.Request) ([]completion.Item, error))
}

// LocalRuntime 增加本地交互结束观察能力。
type LocalRuntime interface {
	Runtime
	DisplayTarget
	WaitExit(context.Context) error
}

// NoticePresenter 接收根包完成 binding 过滤后的通知事实。
type NoticePresenter interface {
	PresentNotice(context.Context, Notice) error
}

// StreamRuntime 为没有原始会话目标的本地输入创建消息流。
type StreamRuntime interface {
	StartStream(context.Context) (delivery.MessageStream, error)
}

// Media 是远程 CLI 使用的最小归档与持有能力。
type Media interface {
	ImportReader(context.Context, io.Reader, int64, media.Input, media.ImportLimits) (media.MediaInfo, error)
	Hold(context.Context, string) (*media.HeldMedia, error)
	PublishUsageFinished(context.Context, []media.UsageID)
}

// Notice 是根包完成 binding 过滤后交给实现呈现的通知事实。
type Notice struct {
	Text        string
	Level       string
	Target      events.NotificationTarget
	Destination delivery.Target
}
