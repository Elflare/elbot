package agent

import (
	"context"
	"strings"

	agentevents "elbot/internal/agent/events"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/delivery/dispatch"
	globalevents "elbot/internal/events"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/platform"
	"elbot/internal/request"
	"elbot/internal/signal"
	"elbot/internal/storage"
)

type hookRunner interface {
	Run(context.Context, hook.Event) (hook.Event, globalevents.LogResult, error)
	Notify(context.Context, hook.Event) (globalevents.LogResult, error)
}

type HookRouter interface {
	Cancel(hook.Event) bool
	Route(context.Context, hook.Event) (hook.Event, bool, error)
	RouteHookID(hook.Event) string
}

type HookBridge struct {
	manager        hookRunner
	router         HookRouter
	requests       *request.Manager
	identity       HookIdentity
	media          *media.Manager
	failed         *signal.Signal[agentevents.HookFailedEvent, signal.Empty]
	dispatcher     *dispatch.Router
	platformEvents platformSubscriptions
}

func (h *HookBridge) CancelRoute(event hook.Event) bool {
	return h.router.Cancel(event)
}

func (h *HookBridge) Route(ctx context.Context, event hook.Event) (hook.Event, bool, error) {
	parent := ctx
	if id := h.router.RouteHookID(event); id != "" {
		requestCtx, done := h.ObserveRun(ctx, event, hook.ObserverInfo{Name: id + " continuation", Point: event.Point})
		defer done()
		ctx = requestCtx
	}
	updated, routed, err := h.router.Route(ctx, event)
	if err != nil && parent.Err() == nil {
		h.NotifyError(ctx, event, err)
		h.publishFailure(ctx, event, err, true)
	}
	return updated, routed, err
}

func (h *HookBridge) Run(ctx context.Context, event hook.Event) (hook.Event, globalevents.LogResult, error) {
	manager := h.manager
	event = h.FillContext(ctx, event)
	before := hook.SnapshotCalls(event)
	updated, logResult, err := manager.Run(ctx, event)
	if err != nil {
		if ctx.Err() != nil {
			return event, logResult, err
		}
		h.NotifyError(ctx, event, err)
		h.publishFailure(ctx, event, err, true)
		return before, logResult, err
	}
	return updated, logResult, nil
}

func (h *HookBridge) Notify(ctx context.Context, event hook.Event) {
	manager := h.manager
	event = h.FillContext(ctx, event)
	if _, err := manager.Notify(ctx, event); err != nil {
		h.publishFailure(ctx, event, err, false)
		if ctx.Err() != nil {
			return
		}
		if event.Point != hook.PointErrorOccurred {
			h.NotifyError(ctx, event, err)
		}
	}
}

// ObserveRun participates synchronously in request tracking.
func (h *HookBridge) ObserveRun(ctx context.Context, event hook.Event, info hook.ObserverInfo) (context.Context, func()) {
	sessionID := strings.TrimSpace(event.Session.ID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(event.Request.SessionID)
	}
	label := strings.TrimSpace(info.Name)
	_, reqCtx, done := h.requests.Start(ctx, request.StartRequest{
		ParentID:  contextinfo.RootRequestIDFromContext(ctx),
		SessionID: sessionID,
		Kind:      request.KindHook,
		Label:     label,
	})
	return reqCtx, done
}

func (h *HookBridge) NotifyError(ctx context.Context, source hook.Event, err error) {
	if source.Point == hook.PointErrorOccurred {
		return
	}
	event := source
	event.Point = hook.PointErrorOccurred
	event.Error = err
	h.Notify(ctx, event)
}

func (h *HookBridge) publishFailure(ctx context.Context, event hook.Event, err error, notice bool) {
	agentevents.Emit(context.WithoutCancel(ctx), h.failed, agentevents.HookFailedEvent{EventMeta: agentevents.Meta(ctx, event.Session.ID), Point: event.Point, Platform: event.Platform, Err: err, Notice: notice})
}

func (h *HookBridge) handlePlatformConnected(ctx context.Context, platformName string) {
	event, _, err := h.Run(ctx, hook.Event{Point: hook.PointPlatformConnected, Platform: hook.PlatformContext{Name: platformName}})
	if err == nil && len(event.Outputs) > 0 {
		_, _, err = h.dispatcher.SendTo(ctx, delivery.Target{}, event.Outputs)
	}
	if err != nil {
		h.publishFailure(ctx, hook.Event{Point: hook.PointPlatformConnected, Platform: hook.PlatformContext{Name: platformName}}, err, false)
	}
}

func hookSession(session *storage.Session) hook.SessionContext {
	if session == nil {
		return hook.SessionContext{}
	}
	return hook.SessionContext{ID: session.ID, Mode: session.Mode, Title: session.Title, Status: session.Status}
}

func (h *HookBridge) FillContext(ctx context.Context, event hook.Event) hook.Event {
	event.Media = h.media
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
	return hook.ActorContext{
		ID:          actor.ID,
		Role:        string(actor.Role),
		GroupRole:   string(actor.GroupRole),
		UserID:      actor.PlatformUserID,
		Nickname:    actor.Nickname,
		GroupCard:   actor.GroupCard,
		DisplayName: actor.DisplayName,
	}
}

// NewHookBridge initializes HookBridge without installing cross-service callbacks.
func NewHookBridge(deps HookBridgeDeps) *HookBridge {
	return &HookBridge{
		manager:    deps.Manager,
		router:     deps.Router,
		requests:   deps.Requests,
		identity:   deps.Identity,
		media:      deps.Media,
		failed:     deps.Failed,
		dispatcher: deps.Dispatcher,
	}
}

func (h *HookBridge) HasRoute(event hook.Event) bool {
	return h.router.RouteHookID(event) != ""
}
