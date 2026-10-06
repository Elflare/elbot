package agent

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	agentevents "elbot/internal/agent/events"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery/dispatch"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/media"
	notificationrules "elbot/internal/notification/rules"
	"elbot/internal/platform"
	"elbot/internal/request"
	"elbot/internal/signal"
	"elbot/internal/storage"
)

type hookRunner interface {
	Run(context.Context, hook.Event) (hook.Event, error)
	Notify(context.Context, hook.Event) error
}

type HookRouter interface {
	Cancel(hook.Event) bool
	Route(context.Context, hook.Event) (hook.Event, bool, error)
	RouteHookID(hook.Event) string
}

type hookBridge struct {
	manager    hookRunner
	router     HookRouter
	requests   *request.Manager
	identity   *identityResolver
	media      *media.Manager
	failed     *signal.Signal[agentevents.HookFailedEvent]
	dispatcher *dispatch.Router
	logger     *slog.Logger
}

func (h *hookBridge) CancelRoute(event hook.Event) bool {
	return h.router != nil && h.router.Cancel(event)
}

func (h *hookBridge) Route(ctx context.Context, event hook.Event) (hook.Event, bool, error) {
	if h.router == nil {
		return event, false, nil
	}
	if id := h.router.RouteHookID(event); id != "" && h.requests != nil {
		_, requestCtx, done, err := h.requests.Start(ctx, request.StartRequest{ParentID: contextinfo.RootRequestIDFromContext(ctx), Kind: request.KindHook, Label: id + " continuation"})
		if err == nil {
			defer done()
			ctx = requestCtx
		}
	}
	return h.router.Route(ctx, event)
}

func (h *hookBridge) Run(ctx context.Context, event hook.Event) (hook.Event, error) {
	manager := h.manager
	if manager == nil {
		manager = hook.NoopManager{}
	}
	event = h.fillContext(ctx, event)
	before := hook.SnapshotCalls(event)
	updated, err := manager.Run(ctx, event)
	if err == nil {
		err = hook.ValidateCalls(ctx, before, updated)
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return event, err
		}
		h.notifyError(ctx, event, err)
		h.publishFailure(ctx, event, err, false, true)
		return before, err
	}
	return updated, nil
}

func (h *hookBridge) Notify(ctx context.Context, event hook.Event) {
	manager := h.manager
	if manager == nil {
		manager = hook.NoopManager{}
	}
	event = h.fillContext(ctx, event)
	if err := manager.Notify(ctx, event); err != nil {
		h.publishFailure(ctx, event, err, true, !errors.Is(err, context.Canceled) && event.Point != hook.PointErrorOccurred)
		if errors.Is(err, context.Canceled) {
			return
		}
		if event.Point != hook.PointErrorOccurred {
			h.notifyError(ctx, event, err)
		}
	}
}

// ObserveRun participates synchronously in request tracking.
func (h *hookBridge) ObserveRun(ctx context.Context, event hook.Event, info hook.ObserverInfo) (context.Context, func()) {
	if h == nil || h.requests == nil {
		return ctx, func() {}
	}
	sessionID := strings.TrimSpace(event.Session.ID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(event.Request.SessionID)
	}
	label := strings.TrimSpace(info.Name)
	if label == "" {
		label = strings.TrimSpace(string(info.Point))
	}
	_, reqCtx, done, err := h.requests.Start(ctx, request.StartRequest{
		ParentID:  contextinfo.RootRequestIDFromContext(ctx),
		SessionID: sessionID,
		Kind:      request.KindHook,
		Label:     label,
	})
	if err != nil {
		if h.logger != nil {
			h.logger.WarnContext(ctx, "hook request tracking failed", "hook", label, "point", string(info.Point), "error", err.Error())
		}
		return ctx, func() {}
	}
	return reqCtx, done
}

func (h *hookBridge) notifyError(ctx context.Context, source hook.Event, err error) {
	if source.Point == hook.PointErrorOccurred {
		return
	}
	event := source
	event.Point = hook.PointErrorOccurred
	event.Error = err
	h.Notify(ctx, event)
}

func (h *hookBridge) publishFailure(ctx context.Context, event hook.Event, err error, log, notice bool) {
	agentevents.Emit(ctx, h.failed, agentevents.HookFailedEvent{EventMeta: agentevents.Meta(ctx, event.Session.ID), Point: event.Point, Platform: event.Platform, Err: err, Log: log, Notice: notice})
}

func (h *hookBridge) PlatformConnected(ctx context.Context, platformName string) {
	if err := notificationrules.PlatformConnected(ctx, platformName, h.Run, h.dispatcher); err != nil {
		h.publishFailure(ctx, hook.Event{Point: hook.PointPlatformConnected, Platform: hook.PlatformContext{Name: platformName}}, err, true, false)
	}
}

func hookSession(session *storage.Session) hook.SessionContext {
	if session == nil {
		return hook.SessionContext{}
	}
	return hook.SessionContext{ID: session.ID, Mode: session.Mode, Title: session.Title, Status: session.Status}
}

func (h *hookBridge) fillContext(ctx context.Context, event hook.Event) hook.Event {
	if h.media != nil {
		event.Media = h.media
	}
	// Connection and other source-free events must not inherit the Agent's
	// default local identity. Only message facts or an explicit actor apply.
	actor := h.identity.SourceActor(ctx)
	if info, ok := contextinfo.ConversationFromContext(ctx); ok {
		if event.Platform.Name == "" {
			event.Platform.Name = info.Source.Platform
		}
		if event.Platform.ScopeID == "" {
			event.Platform.ScopeID = info.Source.ScopeID
		}
		if event.Platform.ConversationID == "" {
			event.Platform.ConversationID = info.Source.ConversationID
		}
		if event.Platform.PlatformMessageID == "" {
			event.Platform.PlatformMessageID = info.PlatformMessageID
		}
		if event.Platform.ReplyToMessageID == "" {
			event.Platform.ReplyToMessageID = info.ReplyToMessageID
		}
	}
	if msg, ok := platform.MessageContextFrom(ctx); ok {
		if event.Message.PlatformText == "" {
			event.Message.PlatformText = msg.RawText
		}
		if event.Point == hook.PointPlatformMessageReceived && len(event.Message.PlatformMessage) == 0 && len(msg.PlatformMessage) > 0 {
			event.Message.PlatformMessage = append(event.Message.PlatformMessage, msg.PlatformMessage...)
		}
		if event.Message.Reply == nil && msg.Reply.MessageID != "" {
			replySegments := platformSegmentsToLLM(msg.Reply.Segments, msg.Reply.Text)
			event.Message.Reply = &hook.MessageReplyPayload{
				MessageID:   msg.Reply.MessageID,
				SenderID:    msg.Reply.SenderID,
				Text:        llm.SegmentsTextOnly(replySegments),
				DisplayText: llm.SegmentsContentText(replySegments),
				Segments:    replySegments,
			}
		}
	}
	if event.Message.IntentText == "" && event.Message.Role == string(llm.RoleUser) {
		event.Message.IntentText = stripWakeupPrefix(ctx, llm.SegmentsTextOnly(event.Message.Segments))
	}
	if event.Platform.UserID == "" {
		event.Platform.UserID = actor.PlatformUserID
	}
	if event.Actor.ID == "" {
		event.Actor = actorContext(actor)
	} else if event.Actor.DisplayName == "" {
		event.Actor.DisplayName = actor.DisplayName
	}
	return event
}

func actorContext(actor contextinfo.Actor) hook.ActorContext {
	return hook.ActorContext{ID: actor.ID, Role: string(actor.Role), GroupRole: string(actor.GroupRole), UserID: actor.PlatformUserID, Nickname: actor.Nickname, GroupCard: actor.GroupCard, DisplayName: actor.DisplayName}
}

func (a *Agent) ObserveHookRun(ctx context.Context, event hook.Event, info hook.ObserverInfo) (context.Context, func()) {
	if a == nil || a.hooks == nil {
		return ctx, func() {}
	}
	return a.hooks.ObserveRun(ctx, event, info)
}

func (a *Agent) NotifyPlatformConnected(ctx context.Context, platformName string) {
	a.hooks.PlatformConnected(ctx, platformName)
}
