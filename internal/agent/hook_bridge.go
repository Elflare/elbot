package agent

import (
	"context"
	"elbot/internal/agent/hooks"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/request"
	"strings"
)

// RegisterHooks accepts one provider. Detaching does not interrupt callbacks
// already running under their original contexts.
func (m *Supervisor) RegisterHooks(points Points) (func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, context.Canceled
	}
	callbacks := hooks.Points{}
	facts := func(value hooks.Facts) Facts {
		return Facts{Session: Session(value.Session), Actor: value.Actor, Woken: value.Woken, ReadOnlyCalls: value.ReadOnlyCalls}
	}
	if points.HasRoute != nil {
		callbacks.HasRoute = func(ctx context.Context, valueFacts hooks.Facts, value agentshare.Message) bool {
			return points.HasRoute(ctx, facts(valueFacts), Message(value))
		}
	}
	if points.CancelRoute != nil {
		callbacks.CancelRoute = func(ctx context.Context, valueFacts hooks.Facts, value agentshare.Message) bool {
			return points.CancelRoute(ctx, facts(valueFacts), Message(value))
		}
	}
	if points.PlatformMessageReceived != nil {
		callbacks.PlatformMessageReceived = func(ctx context.Context, valueFacts hooks.Facts, value agentshare.Message) (agentshare.PlatformMessageReceivedResult, events.LogResult, error) {
			valueResult, logResult, err := points.PlatformMessageReceived(ctx, facts(valueFacts), Message(value))
			return agentshare.PlatformMessageReceivedResult(valueResult), logResult, err
		}
	}
	if points.AgentInputPrepared != nil {
		callbacks.AgentInputPrepared = func(ctx context.Context, valueFacts hooks.Facts, value agentshare.Message) ([]llm.MessageSegment, events.LogResult, error) {
			return points.AgentInputPrepared(ctx, facts(valueFacts), Message(value))
		}
	}
	if points.LLMTurnPrepared != nil {
		callbacks.LLMTurnPrepared = func(ctx context.Context, valueFacts hooks.Facts, value agentshare.LLMInput) (agentshare.LLMInputResult, events.LogResult, error) {
			valueResult, logResult, err := points.LLMTurnPrepared(ctx, facts(valueFacts), LLMInput{Message: Message(value.Message), Provider: value.Provider, Model: value.Model, Messages: value.Messages, Tools: value.Tools})
			return agentshare.LLMInputResult(valueResult), logResult, err
		}
	}
	if points.LLMRequestPrepared != nil {
		callbacks.LLMRequestPrepared = func(ctx context.Context, valueFacts hooks.Facts, value agentshare.LLMInput) (agentshare.LLMInputResult, events.LogResult, error) {
			valueResult, logResult, err := points.LLMRequestPrepared(ctx, facts(valueFacts), LLMInput{Message: Message(value.Message), Provider: value.Provider, Model: value.Model, Messages: value.Messages, Tools: value.Tools})
			return agentshare.LLMInputResult(valueResult), logResult, err
		}
	}
	if points.LLMResponseReceived != nil {
		callbacks.LLMResponseReceived = func(ctx context.Context, valueFacts hooks.Facts, value agentshare.LLMResponse) (agentshare.LLMResponseResult, events.LogResult, error) {
			valueResult, logResult, err := points.LLMResponseReceived(ctx, facts(valueFacts), LLMResponse(value))
			return agentshare.LLMResponseResult(valueResult), logResult, err
		}
	}
	if points.ToolCallPrepared != nil {
		callbacks.ToolCallPrepared = func(ctx context.Context, valueFacts hooks.Facts, value llm.ToolCallRequest) (string, events.LogResult, error) {
			return points.ToolCallPrepared(ctx, facts(valueFacts), value)
		}
	}
	if points.ToolCallCompleted != nil {
		callbacks.ToolCallCompleted = func(ctx context.Context, valueFacts hooks.Facts, value agentshare.ToolResult) ([]llm.MessageSegment, events.LogResult, error) {
			return points.ToolCallCompleted(ctx, facts(valueFacts), ToolResult(value))
		}
	}
	if points.AgentOutputPrepared != nil {
		callbacks.AgentOutputPrepared = func(ctx context.Context, valueFacts hooks.Facts, value string) (string, events.LogResult, error) {
			return points.AgentOutputPrepared(ctx, facts(valueFacts), value)
		}
	}
	if points.AgentTurnOutputPrepared != nil {
		callbacks.AgentTurnOutputPrepared = func(ctx context.Context, valueFacts hooks.Facts, value string) (string, events.LogResult, error) {
			return points.AgentTurnOutputPrepared(ctx, facts(valueFacts), value)
		}
	}
	if points.PlatformMessageSent != nil {
		callbacks.PlatformMessageSent = func(ctx context.Context, valueFacts hooks.Facts, value string) {
			points.PlatformMessageSent(ctx, facts(valueFacts), value)
		}
	}
	if points.ErrorOccurred != nil {
		callbacks.ErrorOccurred = func(ctx context.Context, valueFacts hooks.Facts, response agentshare.LLMResponse, err error) {
			points.ErrorOccurred(ctx, facts(valueFacts), LLMResponse(response), err)
		}
	}
	return m.bridge.Register(callbacks)
}

// StartHookRequest tracks one actual handler, not an entire callback group.
// Empty session IDs are valid before input binding. Provider-global work that
// does not participate in Agent execution must not use this entrypoint.
func (m *Supervisor) StartHookRequest(ctx context.Context, sessionID, name string) (context.Context, func()) {
	_, runCtx, done := m.requests.Start(ctx, request.StartRequest{
		ParentID:  contextinfo.RootRequestIDFromContext(ctx),
		SessionID: strings.TrimSpace(sessionID),
		Kind:      request.KindHook,
		Label:     strings.TrimSpace(name),
	})
	return runCtx, done
}
