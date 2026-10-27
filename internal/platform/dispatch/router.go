// router.go 选择目标适配器并路由发送请求，协调实际投递与回执。

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
	"elbot/internal/platform"
	"elbot/internal/storage"
)

type Options struct {
	Primary interface {
		Name() string
		delivery.MessageSender
	}
	Store              storage.Store
	Media              *media.Manager
	MediaRetentionDays int
}

type Router struct {
	primary interface {
		Name() string
		delivery.MessageSender
	}
	mu                 sync.RWMutex
	senders            map[string]delivery.MessageSender
	store              storage.Store
	media              *media.Manager
	mediaRetentionDays int
}

func New(opts Options) *Router {
	r := &Router{primary: opts.Primary, senders: make(map[string]delivery.MessageSender), store: opts.Store, media: opts.Media, mediaRetentionDays: opts.MediaRetentionDays}
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
		if msg, ok := platform.MessageContextFrom(ctx); ok && msg.Sender != nil {
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
