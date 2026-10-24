package input

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"strings"

	"elbot/internal/agent/dialogue"
	"elbot/internal/agent/hooks"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/security"
	"elbot/internal/storage"
)

type MessageHandler struct {
	identity  MessageIdentity
	hooks     MessageHooks
	output    MessageOutput
	commands  MessageCommands
	input     MessageInput
	media     *media.Manager
	messages  storage.MessageRepository
	mediaRows storage.MediaRepository
}

// HandleMessage dispatches commands and chat messages.
func (h *MessageHandler) HandleMessage(ctx context.Context, text string) (err error) {
	actor := h.identity.Actor(ctx)
	ctx = security.WithPermission(contextinfo.WithActor(ctx, actor), h.identity.Permission())
	segments := agentshare.InboundSegments(ctx, text)
	woken := h.messageWakeup(ctx, llm.SegmentsTextOnly(segments))
	ctx = hooks.WithWakeup(ctx, woken)
	message := hooks.Message{Role: string(llm.RoleUser), Segments: segments}
	if strings.TrimSpace(llm.SegmentsTextOnly(segments)) == "/cancel" {
		if h.hooks.CancelRoute(ctx, message) {
			return h.output.SendChat(ctx, "已取消当前 Hook 会话。")
		}
	}
	waiting := h.hooks.HasRoute(ctx, message)
	if woken || waiting {
		ctx, err = h.materializePlatformMedia(ctx)
		if err != nil {
			if ctx.Err() == nil {
				dialogue.ReportFailure(ctx, "", fmt.Errorf("接收媒体: %w", err), h.output.Notify)
			}
			return err
		}
		segments = agentshare.InboundSegments(ctx, text)
		message.Segments = segments
	}
	result, _, err := h.hooks.PlatformMessageReceived(ctx, message)
	if err != nil {
		if ctx.Err() == nil {
			dialogue.ReportFailure(ctx, "", fmt.Errorf("入站 Hook: %w", err), h.output.Notify)
		}
		return err
	}
	if len(result.Outputs) > 0 {
		if _, err := h.output.SendOutputs(ctx, result.Outputs); err != nil {
			if ctx.Err() == nil {
				dialogue.ReportFailure(ctx, "", fmt.Errorf("发送入站 Hook 输出: %w", err), h.output.Notify)
			}
			return err
		}
	}
	if result.Consume {
		return nil
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
		return h.output.SendChat(ctx, "已取消当前 Hook 会话。")
	}
	if !woken {
		return nil
	}
	text = agentshare.StripWakeupPrefix(ctx, text)
	segments = agentshare.ReplaceInboundTextSegments(ctx, text)
	ctx = agentshare.WithInboundSegments(ctx, segments)
	if !hasForkFromMessage(ctx) {
		if err := h.input.ExpireIdleCurrentSession(ctx); err != nil {
			return err
		}
	}
	if handled, err := h.commands.Handle(ctx, text); handled {
		return err
	}
	return h.input.HandleInput(ctx, text)
}

type MessageIdentity interface {
	dialogue.Identity
	Permission() *security.Permission
}

type MessageHooks interface {
	CancelRoute(context.Context, hooks.Message) bool
	HasRoute(context.Context, hooks.Message) bool
	PlatformMessageReceived(context.Context, hooks.Message) (hooks.PlatformMessageReceivedResult, events.LogResult, error)
}

type MessageOutput interface {
	SendChat(context.Context, string) error
	SendOutputs(context.Context, []delivery.Output) (events.LogResult, error)
	Notify(context.Context, slog.Level, string)
}

type MessageCommands interface {
	Handle(context.Context, string) (bool, error)
	IsCommand(string) bool
}

type MessageInput interface {
	HandleInput(context.Context, string) error
	ExpireIdleCurrentSession(context.Context) error
}

// MessageHandlerDeps declares only the capabilities consumed by MessageHandler.
type MessageHandlerDeps struct {
	Identity  MessageIdentity
	Hooks     MessageHooks
	Output    MessageOutput
	Commands  MessageCommands
	Input     MessageInput
	Media     *media.Manager
	Messages  storage.MessageRepository
	MediaRows storage.MediaRepository
}

// NewMessageHandler initializes MessageHandler without installing cross-service callbacks.
func NewMessageHandler(deps MessageHandlerDeps) *MessageHandler {
	return &MessageHandler{
		identity:  deps.Identity,
		hooks:     deps.Hooks,
		output:    deps.Output,
		commands:  deps.Commands,
		input:     deps.Input,
		media:     deps.Media,
		messages:  deps.Messages,
		mediaRows: deps.MediaRows,
	}
}
