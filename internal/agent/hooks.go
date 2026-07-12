package agent

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"elbot/internal/delivery"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/platform"
	"elbot/internal/request"
	"elbot/internal/security"
	"elbot/internal/storage"
)

type hookRunner interface {
	Run(context.Context, hook.Event) (hook.Event, error)
	Notify(context.Context, hook.Event) error
}

type hookRouter interface {
	Cancel(hook.Event) bool
	Route(context.Context, hook.Event) (hook.Event, bool, error)
	RouteHookID(hook.Event) string
}

func (a *Agent) SetHookManager(manager hook.Manager) {
	if manager == nil {
		manager = hook.NoopManager{}
	}
	if defaultManager, ok := manager.(*hook.DefaultManager); ok {
		defaultManager.SetWakeupFunc(a.hookWakeup)
		defaultManager.SetObserver(a.observeHookRun)
	}
	a.hooks = manager
}

// SetHookRuntime attaches stateful Hook continuation routing. Process lifecycle
// management remains outside Agent in the Hook control service.
func (a *Agent) SetHookRuntime(router hookRouter) {
	a.hookRuntime = router
}

func (a *Agent) cancelHookRoute(event hook.Event) bool {
	return a.hookRuntime != nil && a.hookRuntime.Cancel(event)
}

func (a *Agent) routeHook(ctx context.Context, event hook.Event) (hook.Event, bool, error) {
	if a.hookRuntime == nil {
		return event, false, nil
	}
	if id := a.hookRuntime.RouteHookID(event); id != "" && a.requests != nil {
		_, requestCtx, done, err := a.requests.Start(ctx, request.StartRequest{ParentID: turnRequestIDFromContext(ctx), Kind: request.KindHook, Label: id + " continuation"})
		if err == nil {
			defer done()
			ctx = requestCtx
		}
	}
	return a.hookRuntime.Route(ctx, event)
}

func (a *Agent) runHook(ctx context.Context, event hook.Event) (hook.Event, error) {
	manager := a.hooks
	if manager == nil {
		manager = hook.NoopManager{}
	}
	event = a.fillHookContext(ctx, event)
	updated, err := manager.Run(ctx, event)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return event, err
		}
		a.notifyHookError(ctx, event, err)
		a.sendHookFailureNotice(ctx, event, err)
		return event, err
	}
	return updated, nil
}

func (a *Agent) notifyHook(ctx context.Context, event hook.Event) {
	manager := a.hooks
	if manager == nil {
		manager = hook.NoopManager{}
	}
	event = a.fillHookContext(ctx, event)
	if err := manager.Notify(ctx, event); err != nil {
		a.logHookError(event.Point, err)
		if errors.Is(err, context.Canceled) {
			return
		}
		if event.Point != hook.PointErrorOccurred {
			a.notifyHookError(ctx, event, err)
			a.sendHookFailureNotice(ctx, event, err)
		}
	}
}

func (a *Agent) observeHookRun(ctx context.Context, event hook.Event, info hook.ObserverInfo) (context.Context, func()) {
	if a == nil || a.requests == nil {
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
	_, reqCtx, done, err := a.requests.Start(ctx, request.StartRequest{
		ParentID:  turnRequestIDFromContext(ctx),
		SessionID: sessionID,
		Kind:      request.KindHook,
		Label:     label,
	})
	if err != nil {
		if a.logger != nil {
			a.logger.WarnContext(ctx, "hook request tracking failed", "hook", label, "point", string(info.Point), "error", err.Error())
		}
		return ctx, func() {}
	}
	return reqCtx, done
}

func (a *Agent) notifyHookError(ctx context.Context, source hook.Event, err error) {
	if source.Point == hook.PointErrorOccurred {
		return
	}
	event := source
	event.Point = hook.PointErrorOccurred
	event.Error = err
	a.notifyHook(ctx, event)
}

func (a *Agent) sendHookFailureNotice(ctx context.Context, event hook.Event, err error) {
	if err == nil || event.Point == hook.PointErrorOccurred || errors.Is(err, context.Canceled) {
		return
	}
	text := hookFailureNoticeText(event, err)
	if strings.TrimSpace(text) == "" {
		return
	}
	target := delivery.Target{}
	if _, ok := platform.MessageContextFrom(ctx); !ok {
		target.Platform = event.Platform.Name
		target.ScopeID = event.Platform.ScopeID
	}
	if sendErr := a.sendNotice(ctx, target, []delivery.Output{delivery.Text(text)}); sendErr != nil && a.logger != nil {
		a.logger.WarnContext(ctx, "hook failure notice failed", "point", string(event.Point), "error", sendErr.Error())
	}
}

func hookFailureNoticeText(event hook.Event, err error) string {
	point := strings.TrimSpace(string(event.Point))
	if point == "" {
		point = "unknown"
	}
	return "Hook 执行失败（" + point + "）：\n" + trimHookNoticeText(err.Error())
}

func trimHookNoticeText(text string) string {
	text = strings.TrimSpace(text)
	const max = 1200
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max]) + "\n...（已截断）"
}

func (a *Agent) NotifyPlatformConnected(ctx context.Context, platformName string) {
	event, err := a.runHook(ctx, hook.Event{Point: hook.PointPlatformConnected, Platform: hook.PlatformContext{Name: platformName}})
	if err != nil {
		a.logHookError(hook.PointPlatformConnected, err)
		return
	}
	if len(event.Outputs) > 0 {
		if err := a.sendOutputs(ctx, event.Outputs); err != nil {
			a.logHookError(hook.PointPlatformConnected, err)
		}
	}
}

func (a *Agent) hookSession(session *storage.Session) hook.SessionContext {
	if session == nil {
		return hook.SessionContext{}
	}
	return hook.SessionContext{ID: session.ID, Mode: session.Mode, Title: session.Title, Status: session.Status}
}

func (a *Agent) fillHookContext(ctx context.Context, event hook.Event) hook.Event {
	actor := a.actor(ctx)
	platformName := a.platform.Name()
	scopeID := a.scopeID
	if msg, ok := platform.MessageContextFrom(ctx); ok {
		if msg.Platform != "" {
			platformName = msg.Platform
		}
		if msg.ScopeID != "" {
			scopeID = msg.ScopeID
		}
		if event.Platform.PlatformMessageID == "" {
			event.Platform.PlatformMessageID = msg.PlatformMessageID
		}
		if event.Platform.ReplyToMessageID == "" {
			event.Platform.ReplyToMessageID = msg.ReplyToMessageID
		}
		if event.Message.PlatformText == "" {
			event.Message.PlatformText = msg.RawText
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
		event.Message.IntentText = a.stripWakeupPrefix(ctx, llm.SegmentsTextOnly(event.Message.Segments))
	}
	if event.Platform.Name == "" {
		event.Platform.Name = platformName
	}
	if event.Platform.ScopeID == "" {
		event.Platform.ScopeID = scopeID
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

func (a *Agent) logHookError(point hook.Point, err error) {
	if err == nil {
		return
	}
	if a.logger != nil {
		if errors.Is(err, context.Canceled) {
			a.logger.Info("hook canceled", slog.String("point", string(point)), slog.String("error", err.Error()))
			return
		}
		a.logger.Warn("hook error", slog.String("point", string(point)), slog.String("error", err.Error()))
	}
}

func actorContext(actor security.Actor) hook.ActorContext {
	return hook.ActorContext{ID: actor.ID, Role: string(actor.Role), GroupRole: string(actor.GroupRole), UserID: actor.PlatformUserID, Nickname: actor.Nickname, GroupCard: actor.GroupCard, DisplayName: actor.DisplayName}
}
