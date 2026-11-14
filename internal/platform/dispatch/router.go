// router.go 管理平台注册、发送目标选择及普通与流式发送路由，只借用发送能力，不接管适配器生命周期。

// Package dispatch owns physical delivery, platform routing and output media.
package dispatch

import (
	"context"
	"elbot/internal/operationresult"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/events"
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
		Info(context.Context, string) (operationresult.Result[media.MediaInfo], error)
		ImportURL(context.Context, string, media.Input, media.ImportLimits) (operationresult.Result[media.MediaInfo], error)
		ImportFile(context.Context, string, media.Input, media.ImportLimits) (operationresult.Result[media.MediaInfo], error)
		ImportBytes(context.Context, []byte, media.Input, media.ImportLimits) (operationresult.Result[media.MediaInfo], error)
		ResolveForOutput(context.Context, string) (operationresult.Result[delivery.Source], media.UsageID, error)
	}
	MediaRetentionDays int
}

type Router struct {
	publishUsageFinished func(context.Context, []media.UsageID) // 实际发送结束后的事实通知。
	primary              string
	mu                   sync.RWMutex
	senders              map[string]delivery.MessageSender
	saveMediaOutput      func(context.Context, storage.MediaOutput) error
	media                interface {
		Info(context.Context, string) (operationresult.Result[media.MediaInfo], error)
		ImportURL(context.Context, string, media.Input, media.ImportLimits) (operationresult.Result[media.MediaInfo], error)
		ImportFile(context.Context, string, media.Input, media.ImportLimits) (operationresult.Result[media.MediaInfo], error)
		ImportBytes(context.Context, []byte, media.Input, media.ImportLimits) (operationresult.Result[media.MediaInfo], error)
		ResolveForOutput(context.Context, string) (operationresult.Result[delivery.Source], media.UsageID, error)
	}
	mediaRetentionDays int
}

func New(opts Options) *Router {
	r := &Router{primary: opts.Primary.Name(), senders: make(map[string]delivery.MessageSender), saveMediaOutput: opts.SaveMediaOutput, media: opts.Media, mediaRetentionDays: opts.MediaRetentionDays}
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

func (r *Router) sender(ctx context.Context, target delivery.Target) operationresult.Result[delivery.MessageSender] {
	if target.Empty() {
		if msg, ok := share.MessageContextFrom(ctx); ok && msg.Sender != nil {
			return operationresult.Result[delivery.MessageSender]{Status: operationresult.Success, Data: msg.Sender}
		}
	}
	name := strings.TrimSpace(target.Platform)
	if name == "" {
		if info, ok := contextinfo.ConversationFromContext(ctx); ok {
			name = strings.TrimSpace(info.Source.Platform)
		}
	}
	if name == "" {
		name = r.primary
	}
	r.mu.RLock()
	sender := r.senders[name]
	r.mu.RUnlock()
	if sender == nil {
		return operationresult.Result[delivery.MessageSender]{Status: operationresult.Unavailable, Message: fmt.Sprintf("target platform %q is not configured", name)}
	}
	return operationresult.Result[delivery.MessageSender]{Status: operationresult.Success, Data: sender}
}

func (r *Router) SendTo(ctx context.Context, target delivery.Target, outputs []delivery.Output) (operationresult.Result[delivery.Receipt], error) {
	if err := ctx.Err(); err != nil {
		return operationresult.Result[delivery.Receipt]{}, err
	}
	if len(outputs) == 0 {
		return operationresult.Result[delivery.Receipt]{Status: operationresult.Success}, nil
	}
	if err := delivery.ValidateOutputs(outputs); err != nil {
		return operationresult.Result[delivery.Receipt]{Status: operationresult.InvalidArguments, Message: err.Error()}, nil
	}
	selected := r.sender(ctx, target)
	if selected.Status != operationresult.Success {
		return operationresult.Result[delivery.Receipt]{Status: selected.Status, Message: selected.Message}, nil
	}
	sender := selected.Data
	return r.sendPreparedMedia(ctx, outputs, func(resolved []delivery.Output) (delivery.Receipt, error) {
		return sender.SendTo(ctx, target, resolved)
	})
}

func (r *Router) StartStream(ctx context.Context) (operationresult.Result[delivery.MessageStream], error) {
	if err := ctx.Err(); err != nil {
		return operationresult.Result[delivery.MessageStream]{}, err
	}
	selected := r.sender(ctx, delivery.Target{})
	if selected.Status != operationresult.Success {
		return operationresult.Result[delivery.MessageStream]{Status: selected.Status, Message: selected.Message}, nil
	}
	sender := selected.Data
	toolCallEvents, structuredToolCalls := sender.(delivery.ToolCallEventSender)
	structuredToolCalls = structuredToolCalls && toolCallEvents.SupportsToolCallEvents()
	if streaming, ok := sender.(delivery.StreamSender); ok {
		stream, err := streaming.StartStream(ctx)
		if err != nil {
			return operationresult.Result[delivery.MessageStream]{}, err
		}
		return operationresult.Result[delivery.MessageStream]{Status: operationresult.Success, Data: r.routeToolCallEvents(ctx, stream, structuredToolCalls)}, nil
	}
	stream := delivery.StreamFunc(func(ctx context.Context, event delivery.StreamEvent) (delivery.Receipt, error) {
		if event.Type != delivery.StreamSend {
			return delivery.Receipt{}, nil
		}
		return sender.SendTo(ctx, delivery.Target{}, []delivery.Output{{Kind: delivery.KindText, Text: event.Text, Blocks: event.Blocks, Actions: event.Actions}})
	})
	return operationresult.Result[delivery.MessageStream]{Status: operationresult.Success, Data: r.routeToolCallEvents(ctx, stream, structuredToolCalls)}, nil
}

func (r *Router) routeToolCallEvents(ctx context.Context, stream delivery.MessageStream, structured bool) delivery.MessageStream {
	return delivery.StreamFunc(func(ctx context.Context, event delivery.StreamEvent) (delivery.Receipt, error) {
		if event.Type == delivery.StreamToolCall {
			if structured {
				return stream.Send(ctx, event)
			}
			conversation, ok := contextinfo.ConversationFromContext(ctx)
			if !ok || conversation.Source.ChatType == contextinfo.ChatGroup {
				return delivery.Receipt{}, nil
			}
			if conversation.Source.ChatType != contextinfo.ChatPrivate || event.AssistantSpeaking || event.ToolCall == nil {
				return delivery.Receipt{}, nil
			}
			events.Notify(ctx, events.NewNotification(ctx, slog.LevelDebug, share.FormatToolCallNotice(*event.ToolCall)))
			return delivery.Receipt{}, nil
		}
		if event.Type == delivery.StreamToolResult && !structured {
			return delivery.Receipt{}, nil
		}
		return stream.Send(ctx, event)
	})
}
