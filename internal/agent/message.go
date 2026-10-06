package agent

import (
	"context"
	"log/slog"
	"reflect"
	"strings"

	"elbot/internal/agent/dialogue"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/media"
	notificationrules "elbot/internal/notification/rules"
	"elbot/internal/security"
	"elbot/internal/storage"
)

type messageHandler struct {
	identity  *identityResolver
	hooks     *hookBridge
	output    *outputSender
	commands  *commandExecutor
	input     *inputCoordinator
	media     *media.Manager
	messages  storage.MessageRepository
	mediaRows storage.MediaRepository
	logger    *slog.Logger
}

func (a *Agent) HandleMessage(ctx context.Context, text string) error {
	return a.message.HandleMessage(ctx, text)
}

// HandleMessage dispatches commands and chat messages.
func (h *messageHandler) HandleMessage(ctx context.Context, text string) (err error) {
	actor := h.identity.Actor(ctx)
	ctx = security.WithPolicy(security.WithActor(ctx, actor), h.identity.policy)
	segments := inboundSegments(ctx, text)
	defer func() {
		if err != nil {
			h.hooks.notifyError(ctx, hook.Event{Point: hook.PointAgentInputPrepared, Actor: actorContext(actor), Message: hook.MessagePayload{Role: string(llm.RoleUser), Segments: segments}}, err)
			if dialogue.ShouldNotifyUserError(err) {
				h.output.SendChat(ctx, notificationrules.ExecutionFailure(err))
			}
		}
	}()
	woken := h.messageWakeup(ctx, llm.SegmentsTextOnly(segments))
	ctx = withMessageWakeup(ctx, woken)
	if strings.TrimSpace(llm.SegmentsTextOnly(segments)) == "/cancel" {
		cancelEvent := h.hooks.fillContext(ctx, hook.Event{Point: hook.PointPlatformMessageReceived, Actor: actorContext(actor), Message: hook.MessagePayload{Role: string(llm.RoleUser), Segments: segments}})
		if h.hooks.CancelRoute(cancelEvent) {
			h.output.SendChat(ctx, "已取消当前 Hook 会话。")
			return nil
		}
	}
	event := h.hooks.fillContext(ctx, hook.Event{Point: hook.PointPlatformMessageReceived, Actor: actorContext(actor), Message: hook.MessagePayload{Role: string(llm.RoleUser), Segments: segments}})
	waiting := h.hooks.router != nil && h.hooks.router.RouteHookID(event) != ""
	if woken || waiting {
		ctx = h.materializePlatformMedia(ctx)
		segments = inboundSegments(ctx, text)
		event = h.hooks.fillContext(ctx, hook.Event{Point: hook.PointPlatformMessageReceived, Actor: actorContext(actor), Message: hook.MessagePayload{Role: string(llm.RoleUser), Segments: segments}})
	}
	event, routed, routeErr := h.hooks.Route(ctx, event)
	if routeErr != nil {
		return routeErr
	}
	if !routed || !event.Control.StopPropagation {
		event, err = h.hooks.Run(ctx, event)
		if err != nil {
			return err
		}
	}
	if len(event.Outputs) > 0 {
		if err := h.output.SendOutputs(ctx, event.Outputs); err != nil {
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
		h.output.SendChat(ctx, "已取消当前 Hook 会话。")
		return nil
	}
	if !woken {
		return nil
	}
	text = stripWakeupPrefix(ctx, text)
	segments = replaceInboundTextSegments(ctx, text)
	ctx = withInboundSegments(ctx, segments)
	if !hasForkFromMessage(ctx) {
		if err := h.input.expireIdleCurrentSession(ctx); err != nil {
			return err
		}
	}
	if handled, err := h.commands.Handle(ctx, text); handled {
		return err
	}
	return h.input.handleInput(ctx, text)
}
