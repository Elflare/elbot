// Package dispatch owns physical delivery, platform routing and output media.
package dispatch

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"elbot/internal/chatinfo"
	"elbot/internal/delivery"
	"elbot/internal/media"
	"elbot/internal/platform"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/storage"
)

type Options struct {
	Primary            platform.PlatformAdapter
	Store              storage.Store
	Media              *media.Manager
	MediaRetentionDays int
	Logger             *slog.Logger
}

type Router struct {
	primary            platform.PlatformAdapter
	mu                 sync.RWMutex
	senders            map[string]delivery.MessageSender
	store              storage.Store
	media              *media.Manager
	mediaRetentionDays int
	logger             *slog.Logger
}

func New(opts Options) *Router {
	r := &Router{primary: opts.Primary, senders: make(map[string]delivery.MessageSender), store: opts.Store, media: opts.Media, mediaRetentionDays: opts.MediaRetentionDays, logger: opts.Logger}
	if opts.Primary != nil {
		r.RegisterPlatformSender(opts.Primary.Name(), opts.Primary)
	}
	return r
}

func (r *Router) RegisterPlatformSender(name string, sender delivery.MessageSender) {
	name = strings.TrimSpace(name)
	if name == "" || sender == nil {
		return
	}
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
		if info, ok := chatinfo.FromContext(ctx); ok {
			name = strings.TrimSpace(info.Source.Platform)
		}
	}
	if name == "" && r.primary != nil {
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

func (r *Router) SendChat(ctx context.Context, outputs []delivery.Output) (delivery.Receipt, error) {
	return delivery.NewManager(rawSender{r}, r.logger).SendChat(ctx, outputs)
}

func (r *Router) SendNotice(ctx context.Context, notice delivery.Notice) (delivery.Receipt, error) {
	return delivery.NewManager(rawSender{r}, r.logger).SendNotice(ctx, notice)
}

type rawSender struct{ router *Router }

func (s rawSender) SendChat(ctx context.Context, outputs []delivery.Output) (delivery.Receipt, error) {
	sender, err := s.router.sender(ctx, delivery.Target{})
	if err != nil {
		return delivery.Receipt{}, err
	}
	return s.router.sendPreparedMedia(ctx, outputs, func(resolved []delivery.Output) (delivery.Receipt, error) { return sender.SendChat(ctx, resolved) })
}

func (s rawSender) SendNotice(ctx context.Context, notice delivery.Notice) (delivery.Receipt, error) {
	sender, err := s.router.sender(ctx, notice.Target)
	if err != nil {
		return delivery.Receipt{}, err
	}
	return s.router.sendPreparedMedia(ctx, notice.Outputs, func(resolved []delivery.Output) (delivery.Receipt, error) {
		notice.Outputs = resolved
		return sender.SendNotice(ctx, notice)
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

func (r *Router) SendReasoning(ctx context.Context, text string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sender, err := r.sender(ctx, delivery.Target{})
	if err != nil {
		return err
	}
	if reasoning, ok := sender.(interface {
		SendReasoning(context.Context, string) error
	}); ok {
		return reasoning.SendReasoning(ctx, text)
	}
	return nil
}

func (r *Router) SetRuntimeStatus(ctx context.Context, snapshot runtimestatus.Snapshot) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sender, err := r.sender(ctx, delivery.Target{})
	if err != nil {
		return err
	}
	if status, ok := sender.(interface {
		SetRuntimeStatus(context.Context, runtimestatus.Snapshot) error
	}); ok {
		return status.SetRuntimeStatus(ctx, snapshot)
	}
	return nil
}

// RuntimeStatusTarget identifies the actual display without doing platform I/O.
// Remote adapters include their original connection, rather than just a user ID.
func (r *Router) RuntimeStatusTarget(ctx context.Context) (string, error) {
	sender, err := r.sender(ctx, delivery.Target{})
	if err != nil {
		return "", err
	}
	if _, ok := sender.(interface {
		SetRuntimeStatus(context.Context, runtimestatus.Snapshot) error
	}); !ok {
		return "", nil
	}
	if target, ok := sender.(interface{ RuntimeStatusTarget(context.Context) string }); ok {
		return target.RuntimeStatusTarget(ctx), nil
	}
	return fmt.Sprintf("%T:%p", sender, sender), nil
}
