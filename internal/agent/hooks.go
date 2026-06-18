package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"elbot/internal/delivery"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/platform"
	"elbot/internal/security"
	"elbot/internal/storage"
)

func (a *Agent) runHook(ctx context.Context, event hook.Event) (hook.Event, error) {
	manager := a.hooks
	if manager == nil {
		manager = hook.NoopManager{}
	}
	event = a.fillHookContext(ctx, event)
	updated, err := manager.Run(ctx, event)
	if err != nil {
		a.notifyHookError(ctx, event, err)
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
	}
}

func (a *Agent) notifyHookError(ctx context.Context, source hook.Event, err error) {
	if source.Point == hook.PointErrorOccurred {
		a.logHookError(source.Point, err)
		return
	}
	event := source
	event.Point = hook.PointErrorOccurred
	event.Error = err
	a.notifyHook(ctx, event)
}

func (a *Agent) sendOutputs(ctx context.Context, outputs []delivery.Output) error {
	manager := a.outputs
	manager.Sender = agentOutputSender{agent: a, ctx: ctx}
	if manager.Logger == nil {
		manager.Logger = a.logger
	}
	return manager.SendNotices(ctx, outputs)
}

type agentOutputSender struct {
	agent *Agent
	ctx   context.Context
}

func (s agentOutputSender) SendChat(ctx context.Context, out delivery.Output) (delivery.Receipt, error) {
	if s.agent == nil {
		return delivery.Receipt{}, fmt.Errorf("agent output sender is not configured")
	}
	// 优先使用本轮 message context 携带的 sender。
	// QQ 等平台的发送目标可能依赖入站消息解析出的上下文，
	// 退回全局 sender 可能导致通知丢失或发到错误会话。
	if msg, ok := platform.MessageContextFrom(s.ctx); ok && msg.Sender != nil {
		return msg.Sender.SendChat(s.ctx, out)
	}
	if s.agent.platform == nil {
		return delivery.Receipt{}, fmt.Errorf("chat output sender is not configured")
	}
	return s.agent.platform.SendChat(s.ctx, out)
}

func (s agentOutputSender) SendNotice(ctx context.Context, target delivery.Target, out delivery.Output) (delivery.Receipt, error) {
	if s.agent == nil {
		return delivery.Receipt{}, fmt.Errorf("agent output sender is not configured")
	}
	if target.Empty() {
		// 空 target 表示沿用当前会话，必须优先走 message context 的 sender。
		if msg, ok := platform.MessageContextFrom(s.ctx); ok && msg.Sender != nil {
			return msg.Sender.SendNotice(s.ctx, target, out)
		}
	}
	platformName := strings.TrimSpace(target.Platform)
	if platformName == "" {
		if msg, ok := platform.MessageContextFrom(s.ctx); ok {
			platformName = msg.Platform
		}
	}
	if platformName == "" && s.agent.platform != nil {
		platformName = s.agent.platform.Name()
	}
	if platformName == "" {
		return s.SendChat(ctx, out)
	}
	sender := s.agent.platformSenders[platformName]
	if sender == nil {
		return delivery.Receipt{}, fmt.Errorf("target platform %q is not configured", platformName)
	}
	target.Platform = platformName
	return sender.SendNotice(ctx, target, out)
}

type contextTextSender struct {
	ctx    context.Context
	sender delivery.ContextSender
}

func (s contextTextSender) SendChat(ctx context.Context, out delivery.Output) (delivery.Receipt, error) {
	return s.sender.SendChat(s.ctx, out)
}

func (s contextTextSender) SendNotice(ctx context.Context, target delivery.Target, out delivery.Output) (delivery.Receipt, error) {
	return s.sender.SendNotice(s.ctx, target, out)
}

func (a *Agent) sendChat(ctx context.Context, text string) {
	_, _ = a.sendChatWithReceipt(ctx, text)
}

func (a *Agent) sendOutput(ctx context.Context, out delivery.Output) error {
	return a.sendNoticeOutput(ctx, delivery.Target{}, out)
}

func (a *Agent) sendTextOutput(ctx context.Context, text string) error {
	return a.sendNoticeOutput(ctx, delivery.Target{}, delivery.Text(text))
}

func (a *Agent) sendNoticeOutput(ctx context.Context, target delivery.Target, out delivery.Output) error {
	manager := a.outputs
	manager.Sender = agentOutputSender{agent: a, ctx: ctx}
	if manager.Logger == nil {
		manager.Logger = a.logger
	}
	_, err := manager.SendNotice(ctx, target, out)
	return err
}

func (a *Agent) prepareAssistantOutput(ctx context.Context, point hook.Point, text string) (string, error) {
	event, err := a.runHook(ctx, hook.Event{Point: point, Message: hook.MessagePayload{Role: string(llm.RoleAssistant), Segments: llm.TextSegments(text)}})
	if err != nil {
		return "", err
	}
	return llm.SegmentsTextOnly(event.Message.Segments), nil
}

func (a *Agent) sendChatWithReceipt(ctx context.Context, text string) (delivery.Receipt, error) {
	if strings.TrimSpace(text) == "" && bufferAssistantOutput(ctx) {
		return delivery.Receipt{}, nil
	}
	preparedText, err := a.prepareAssistantOutput(ctx, hook.PointAgentOutputPrepared, text)
	if err != nil {
		return delivery.Receipt{}, err
	}
	manager := a.outputs

	manager.Sender = agentOutputSender{agent: a, ctx: ctx}
	if manager.Logger == nil {
		manager.Logger = a.logger
	}
	receipt, err := manager.SendChat(ctx, delivery.Text(preparedText))

	if err != nil {
		if a.logger != nil {
			a.logger.WarnContext(ctx, "chat send failed", "error", err.Error())
		}
		return delivery.Receipt{}, err
	}
	a.notifyHook(ctx, hook.Event{Point: hook.PointPlatformMessageSent, Message: hook.MessagePayload{Role: string(llm.RoleAssistant), Segments: llm.TextSegments(preparedText)}})

	return receipt, nil
}

func bufferAssistantOutput(ctx context.Context) bool {
	msg, ok := platform.MessageContextFrom(ctx)
	return ok && msg.BufferAssistantOutput
}

func (a *Agent) mapSentAssistantMessage(ctx context.Context, sessionID, messageID string, receipt delivery.Receipt) {
	if len(receipt.PlatformMessageIDs) == 0 || a.store == nil || a.store.Messages() == nil {
		return
	}
	scope := a.scope(ctx)
	for _, platformMessageID := range receipt.PlatformMessageIDs {
		platformMessageID = strings.TrimSpace(platformMessageID)
		if platformMessageID == "" {
			continue
		}
		mapping := storage.PlatformMessageMap{
			Platform:          scope.Platform,
			PlatformScopeID:   scope.PlatformScopeID,
			PlatformMessageID: platformMessageID,
			MessageID:         messageID,
			SessionID:         sessionID,
		}
		if err := a.store.Messages().MapPlatformMessage(ctx, mapping); err != nil {
			a.audit("persistence_error", "session_id", sessionID, "operation", "map_platform_message", "platform_message_id", platformMessageID, "error", err.Error())
			if a.logger != nil {
				a.logger.WarnContext(ctx, "map platform message failed", "session_id", sessionID, "platform_message_id", platformMessageID, "error", err.Error())
			}
		}
	}
}

func (a *Agent) RegisterPlatformSender(name string, sender delivery.MessageSender) {
	name = strings.TrimSpace(name)
	if name == "" || sender == nil {
		return
	}
	if a.platformSenders == nil {
		a.platformSenders = map[string]delivery.MessageSender{}
	}
	a.platformSenders[name] = sender
}

func (a *Agent) SendNoticeOutput(ctx context.Context, target delivery.Target, out delivery.Output) (delivery.Receipt, error) {
	manager := a.outputs
	manager.Sender = agentOutputSender{agent: a, ctx: ctx}
	if manager.Logger == nil {
		manager.Logger = a.logger
	}
	return manager.SendNotice(ctx, target, out)
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
		a.logger.Warn("hook error", slog.String("point", string(point)), slog.String("error", err.Error()))
	}
}

func actorContext(actor security.Actor) hook.ActorContext {
	return hook.ActorContext{ID: actor.ID, Role: string(actor.Role), UserID: actor.PlatformUserID, DisplayName: actor.DisplayName}
}
