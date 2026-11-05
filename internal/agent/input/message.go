package input

import (
	"context"
	"log/slog"
	"reflect"
	"strings"
	"time"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/storage"
)

type MessageHandler struct {
	History interface {
		GetByPlatformMessage(context.Context, string, contextinfo.ChatType, string, string) (*storage.ChatMessage, error)
	}
	identity MessageIdentity
	hooks    MessageHooks
	output   MessageOutput
	commands MessageCommands
	input    MessageInput
	media    interface {
		ImportPlatform(context.Context, string, func(context.Context, media.PlatformMedia, int64) (delivery.Source, error), media.PlatformMedia) (media.MediaInfo, error)
		AssociateHistory(context.Context, media.HistoryMessage, int, string, string) error
	}
	messages interface {
		FindByPlatformMessage(context.Context, string, contextinfo.ChatType, string, string) (*storage.Message, error)
	}
	mediaRows interface {
		FindOutputs(context.Context, string, contextinfo.ChatType, string, string, time.Time) ([]storage.MediaOutput, error)
	}
}

// HandleMessage dispatches commands and chat messages.
func (h *MessageHandler) HandleMessage(ctx context.Context, text string) (context.Context, error) {
	actor := h.identity.Actor(ctx)
	ctx = contextinfo.WithActor(ctx, actor)
	segments := agentshare.InboundSegments(ctx, text)
	woken := h.messageWakeup(ctx, llm.SegmentsTextOnly(segments))
	ctx = agentshare.WithWakeup(ctx, woken)
	message := agentshare.Message{Role: string(llm.RoleUser), Segments: segments}
	if strings.TrimSpace(llm.SegmentsTextOnly(segments)) == "/cancel" {
		if h.hooks.CancelRoute(ctx, message) {
			_, err := h.output.Send(ctx, delivery.StreamEvent{Type: delivery.StreamSend, Text: "Hook canceled"})
			return ctx, err
		}
	}
	waiting := h.hooks.HasRoute(ctx, message)
	if woken || waiting {
		prepared, err := h.materializePlatformMedia(ctx)
		if err != nil {
			return ctx, err
		}
		ctx = prepared
		segments = agentshare.InboundSegments(ctx, text)
		message.Segments = segments
	}
	result, err := h.hooks.PlatformMessageReceived(ctx, message)
	if err != nil {
		return ctx, err
	}
	if len(result.Outputs) > 0 {
		if _, err := h.output.Send(ctx, delivery.StreamEvent{Type: delivery.StreamSendExtras, Outputs: result.Outputs}); err != nil {
			return ctx, err
		}
	}
	if result.Consume {
		return ctx, nil
	}
	hookChangedSegments := !reflect.DeepEqual(result.Segments, segments)
	if hookChangedSegments {
		segments = result.Segments
	} else {
		segments = agentshare.InboundContextSegments(ctx, text)
	}
	ctx = agentshare.WithInboundSegments(ctx, segments)
	text = llm.SegmentsTextOnly(segments)
	message.Segments = result.Segments
	if strings.TrimSpace(text) == "/cancel" && h.hooks.CancelRoute(ctx, message) {
		_, err := h.output.Send(ctx, delivery.StreamEvent{Type: delivery.StreamSend, Text: "Hook canceled"})
		return ctx, err
	}
	if !woken {
		return ctx, nil
	}
	text = agentshare.StripWakeupPrefix(ctx, text)
	segments = agentshare.ReplaceInboundTextSegments(ctx, text)
	ctx = agentshare.WithInboundSegments(ctx, segments)
	ctx, handled, err := h.commands.Handle(ctx, text)
	if handled {
		return ctx, err
	}
	return h.input.HandleInput(ctx, text)
}

type MessageIdentity interface {
	Actor(context.Context) contextinfo.Actor
}

type MessageHooks interface {
	CancelRoute(context.Context, agentshare.Message) bool
	HasRoute(context.Context, agentshare.Message) bool
	PlatformMessageReceived(context.Context, agentshare.Message) (agentshare.PlatformMessageReceivedResult, error)
}

type MessageOutput interface {
	Send(context.Context, delivery.StreamEvent) (delivery.Receipt, error)
	Notify(context.Context, slog.Level, string)
}

type MessageCommands interface {
	Handle(context.Context, string) (context.Context, bool, error)
	IsCommand(string) bool
}

type MessageInput interface {
	HandleInput(context.Context, string) (context.Context, error)
}

// MessageHandlerDeps declares only the capabilities consumed by MessageHandler.
type MessageHandlerDeps struct {
	History interface {
		GetByPlatformMessage(context.Context, string, contextinfo.ChatType, string, string) (*storage.ChatMessage, error)
	}
	Identity MessageIdentity
	Hooks    MessageHooks
	Output   MessageOutput
	Commands MessageCommands
	Input    MessageInput
	Media    interface {
		ImportPlatform(context.Context, string, func(context.Context, media.PlatformMedia, int64) (delivery.Source, error), media.PlatformMedia) (media.MediaInfo, error)
		AssociateHistory(context.Context, media.HistoryMessage, int, string, string) error
	}
	Messages interface {
		FindByPlatformMessage(context.Context, string, contextinfo.ChatType, string, string) (*storage.Message, error)
	}
	MediaRows interface {
		FindOutputs(context.Context, string, contextinfo.ChatType, string, string, time.Time) ([]storage.MediaOutput, error)
	}
}

// NewMessageHandler initializes MessageHandler without installing cross-service callbacks.
func NewMessageHandler(deps MessageHandlerDeps) *MessageHandler {
	return &MessageHandler{
		identity:  deps.Identity,
		History:   deps.History,
		hooks:     deps.Hooks,
		output:    deps.Output,
		commands:  deps.Commands,
		input:     deps.Input,
		media:     deps.Media,
		messages:  deps.Messages,
		mediaRows: deps.MediaRows,
	}
}
