package agent

import (
	"context"
	"reflect"
	"strings"

	"elbot/internal/agent/dialogue"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/hook"
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
	ctx = security.WithPolicy(contextinfo.WithActor(ctx, actor), h.identity.Policy())
	segments := inboundSegments(ctx, text)
	woken := h.messageWakeup(ctx, llm.SegmentsTextOnly(segments))
	ctx = withMessageWakeup(ctx, woken)
	if strings.TrimSpace(llm.SegmentsTextOnly(segments)) == "/cancel" {
		cancelEvent := h.hooks.FillContext(ctx, hook.Event{Point: hook.PointPlatformMessageReceived, Actor: actorContext(actor), Message: hook.MessagePayload{Role: string(llm.RoleUser), Segments: segments}})
		if h.hooks.CancelRoute(cancelEvent) {
			return h.output.SendChat(ctx, "已取消当前 Hook 会话。")
		}
	}
	event := h.hooks.FillContext(ctx, hook.Event{Point: hook.PointPlatformMessageReceived, Actor: actorContext(actor), Message: hook.MessagePayload{Role: string(llm.RoleUser), Segments: segments}})
	waiting := h.hooks.HasRoute(event)
	if woken || waiting {
		ctx, err = h.materializePlatformMedia(ctx)
		if err != nil {
			return err
		}
		segments = inboundSegments(ctx, text)
		event = h.hooks.FillContext(ctx, hook.Event{Point: hook.PointPlatformMessageReceived, Actor: actorContext(actor), Message: hook.MessagePayload{Role: string(llm.RoleUser), Segments: segments}})
	}
	event, routed, routeErr := h.hooks.Route(ctx, event)
	if routeErr != nil {
		return routeErr
	}
	if !routed || !event.Control.StopPropagation {
		event, _, err = h.hooks.Run(ctx, event)
		if err != nil {
			return err
		}
	}
	if len(event.Outputs) > 0 {
		if _, err := h.output.SendOutputs(ctx, event.Outputs); err != nil {
			return err
		}
	}
	if event.Control.Consume {
		return nil
	}
	hookChangedSegments := !reflect.DeepEqual(event.Message.Segments, segments)
	if hookChangedSegments {
		segments = event.Message.Segments
	} else {
		segments = inboundContextSegments(ctx, text)
	}
	ctx = withInboundSegments(ctx, segments)
	text = llm.SegmentsTextOnly(segments)
	if strings.TrimSpace(text) == "/cancel" && h.hooks.CancelRoute(event) {
		return h.output.SendChat(ctx, "已取消当前 Hook 会话。")
	}
	if !woken {
		return nil
	}
	text = stripWakeupPrefix(ctx, text)
	segments = replaceInboundTextSegments(ctx, text)
	ctx = withInboundSegments(ctx, segments)
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
	Policy() *security.Policy
}

type MessageHooks interface {
	dialogue.Hooks
	FillContext(context.Context, hook.Event) hook.Event
	CancelRoute(hook.Event) bool
	Route(context.Context, hook.Event) (hook.Event, bool, error)
	HasRoute(hook.Event) bool
}

type MessageOutput interface {
	SendChat(context.Context, string) error
	SendOutputs(context.Context, []delivery.Output) (events.LogResult, error)
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
