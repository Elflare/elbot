package agent

import (
	"context"
	"errors"
	"reflect"
	"strings"

	"elbot/internal/command"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/security"
)

func (a *Agent) CommandInfos() []command.Info {
	if a == nil || a.commands == nil {
		return nil
	}
	return a.commands.Commands()
}

// HandleMessage dispatches commands and chat messages.
func (a *Agent) HandleMessage(ctx context.Context, text string) (err error) {
	actor := a.actor(ctx)
	ctx = security.WithPolicy(security.WithActor(ctx, actor), a.securityPolicy)
	segments := inboundSegments(ctx, text)
	defer func() {
		if err != nil {
			a.notifyHookError(ctx, hook.Event{Point: hook.PointAgentInputPrepared, Actor: actorContext(actor), Message: hook.MessagePayload{Role: string(llm.RoleUser), Segments: segments}}, err)
			if shouldNotifyUserError(err) {
				a.sendChat(ctx, "请求失败："+err.Error())
			}
		}
	}()
	woken := a.messageWakeup(ctx, llm.SegmentsTextOnly(segments))
	if strings.TrimSpace(llm.SegmentsTextOnly(segments)) == "/cancel" {
		cancelEvent := a.fillHookContext(ctx, hook.Event{Point: hook.PointPlatformMessageReceived, Actor: actorContext(actor), Message: hook.MessagePayload{Role: string(llm.RoleUser), Segments: segments}})
		if a.cancelHookRoute(cancelEvent) {
			a.sendChat(ctx, "已取消当前 Hook 会话。")
			return nil
		}
	}
	event := a.fillHookContext(ctx, hook.Event{Point: hook.PointPlatformMessageReceived, Actor: actorContext(actor), Message: hook.MessagePayload{Role: string(llm.RoleUser), Segments: segments}})
	event, routed, routeErr := a.routeHook(ctx, event)
	if routeErr != nil {
		return routeErr
	}
	if !routed || !event.Control.StopPropagation {
		event, err = a.runHook(ctx, event)
		if err != nil {
			return err
		}
	}
	if len(event.Outputs) > 0 {
		if err := a.sendOutputs(ctx, event.Outputs); err != nil {
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
	if strings.TrimSpace(text) == "/cancel" && a.cancelHookRoute(event) {
		a.sendChat(ctx, "已取消当前 Hook 会话。")
		return nil
	}
	if !woken {
		return nil
	}
	text = a.stripWakeupPrefix(ctx, text)
	segments = replaceInboundTextSegments(ctx, text)
	ctx = withInboundSegments(ctx, segments)
	if !hasForkFromMessage(ctx) {
		if err := a.expireIdleCurrentSession(ctx); err != nil {
			return err
		}
	}
	if handled, err := a.commandExecutor.Handle(ctx, text); handled {
		return err
	}
	return a.handleInput(ctx, text)
}

func shouldNotifyUserError(err error) bool {
	var notified userNotifiedError
	return err != nil && !errors.As(err, &notified) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}

type userNotifiedError struct {
	err error
}

func (e userNotifiedError) Error() string { return e.err.Error() }

func (e userNotifiedError) Unwrap() error { return e.err }

func markUserNotified(err error) error {
	if err == nil {
		return nil
	}
	return userNotifiedError{err: err}
}
