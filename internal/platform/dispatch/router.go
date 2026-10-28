// router.go 管理平台注册、发送目标选择及普通与流式发送路由，只借用发送能力，不接管适配器生命周期。

// Package dispatch owns physical delivery, platform routing and output media.
package dispatch

import (
	"context"
	"elbot/internal/events"
	"fmt"
	"strings"
	"sync"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/media"
	"elbot/internal/platform/share"
	"elbot/internal/storage"
)

type Options struct {
	PublishUsageFinished func(context.Context, []media.UsageID) // 根部发布媒体使用结束。
	Primary              interface {
		Name() string
		delivery.MessageSender
	}
	// SaveMediaOutput 关联实际发送消息与媒体，由根部注入借用的存储能力。
	SaveMediaOutput func(context.Context, storage.MediaOutput) error
	Media           interface {
		Info(context.Context, string) (media.MediaInfo, error)
		ImportURL(context.Context, string, media.Input, media.ImportLimits) (media.MediaInfo, error)
		ImportFile(context.Context, string, media.Input, media.ImportLimits) (media.MediaInfo, error)
		ImportBytes(context.Context, []byte, media.Input, media.ImportLimits) (media.MediaInfo, error)
		ResolveForOutput(context.Context, string) (delivery.Source, media.UsageID, error)
	}
	MediaRetentionDays int
}

type Router struct {
	publishUsageFinished func(context.Context, []media.UsageID) // 实际发送结束后的事实通知。
	primary              interface {
		Name() string
		delivery.MessageSender
	}
	mu              sync.RWMutex
	senders         map[string]delivery.MessageSender
	saveMediaOutput func(context.Context, storage.MediaOutput) error
	media           interface {
		Info(context.Context, string) (media.MediaInfo, error)
		ImportURL(context.Context, string, media.Input, media.ImportLimits) (media.MediaInfo, error)
		ImportFile(context.Context, string, media.Input, media.ImportLimits) (media.MediaInfo, error)
		ImportBytes(context.Context, []byte, media.Input, media.ImportLimits) (media.MediaInfo, error)
		ResolveForOutput(context.Context, string) (delivery.Source, media.UsageID, error)
	}
	mediaRetentionDays int
}

func New(opts Options) *Router {
	r := &Router{primary: opts.Primary, senders: make(map[string]delivery.MessageSender), saveMediaOutput: opts.SaveMediaOutput, media: opts.Media, mediaRetentionDays: opts.MediaRetentionDays}
	r.publishUsageFinished = opts.PublishUsageFinished
	r.RegisterPlatform(opts.Primary)
	return r
}

func (r *Router) RegisterPlatform(sender interface {
	Name() string
	delivery.MessageSender
}) {
	name := strings.TrimSpace(sender.Name())
	r.mu.Lock()
	r.senders[name] = sender
	r.mu.Unlock()
}

func (r *Router) sender(ctx context.Context, target delivery.Target) (delivery.MessageSender, error) {
	if target.Empty() {
		if msg, ok := share.MessageContextFrom(ctx); ok && msg.Sender != nil {
			return msg.Sender, nil
		}
	}
	name := strings.TrimSpace(target.Platform)
	if name == "" {
		if info, ok := contextinfo.ConversationFromContext(ctx); ok {
			name = strings.TrimSpace(info.Source.Platform)
		}
	}
	if name == "" {
		name = r.primary.Name()
	}
	r.mu.RLock()
	sender := r.senders[name]
	r.mu.RUnlock()
	if sender == nil {
		return nil, fmt.Errorf("target platform %q is not configured", name)
	}
	return sender, nil
}

func (r *Router) SendChat(ctx context.Context, outputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
	return delivery.NewManager(rawSender{r}).SendChat(ctx, outputs)
}

func (r *Router) SendTo(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
	return delivery.NewManager(rawSender{r}).SendTo(ctx, target, outputs)
}

type rawSender struct{ router *Router }

func (s rawSender) SendChat(ctx context.Context, outputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
	sender, err := s.router.sender(ctx, delivery.Target{})
	if err != nil {
		return delivery.Receipt{}, events.LogResult{ResultStatus: events.ResultFailed, Content: err.Error()}, err
	}
	return s.router.sendPreparedMedia(ctx, outputs, func(resolved []delivery.Output) (delivery.Receipt, events.LogResult, error) {
		return sender.SendChat(ctx, resolved)
	})
}

func (s rawSender) SendTo(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
	sender, err := s.router.sender(ctx, target)
	if err != nil {
		return delivery.Receipt{}, events.LogResult{ResultStatus: events.ResultFailed, Content: err.Error()}, err
	}
	return s.router.sendPreparedMedia(ctx, outputs, func(resolved []delivery.Output) (delivery.Receipt, events.LogResult, error) {
		return sender.SendTo(ctx, target, resolved)
	})
}

func (r *Router) StartStream(ctx context.Context) (delivery.MessageStream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sender, err := r.sender(ctx, delivery.Target{})
	if err != nil {
		return nil, err
	}
	if streaming, ok := sender.(delivery.StreamingMessageSender); ok {
		return streaming.StartStream(ctx)
	}
	return nil, nil
}
