// 注册 Agent 挂接点并适配业务参数、执行结果和逐 handler 请求登记。

package hook

import (
	"context"
	"fmt"
	"reflect"

	agenthooks "elbot/internal/agent"
	"elbot/internal/hook/pipeline"
	"elbot/internal/hook/share"
	"elbot/internal/llm"
)

// ConnectAgent owns one registration. Agent supplies extension points and request
// tracking only; Hook never obtains its request manager or recomputes wakeup.
func (m *Supervisor) ConnectAgent(agent interface {
	RegisterHooks(agenthooks.Points) (func(), error)
	StartHookRequest(context.Context, string, string) (context.Context, func(), error)
}) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return context.Canceled
	}
	if m.disconnect != nil {
		return fmt.Errorf("hook: Agent already connected")
	}
	disconnect, err := agent.RegisterHooks(m.agentPoints(agent))
	if err != nil {
		return err
	}
	m.disconnect = disconnect
	return nil
}

func (m *Supervisor) callOptions(facts agenthooks.Facts, agent interface {
	StartHookRequest(context.Context, string, string) (context.Context, func(), error)
}) pipeline.CallOptions {
	sessionID := facts.Session.ID
	return pipeline.CallOptions{
		Woken:         facts.Woken,
		ReadOnlyCalls: facts.ReadOnlyCalls,
		Observe: func(ctx context.Context, name string) (context.Context, func(), error) {
			return agent.StartHookRequest(ctx, sessionID, name)
		},
	}
}

func (m *Supervisor) agentPoints(agent interface {
	StartHookRequest(context.Context, string, string) (context.Context, func(), error)
}) agenthooks.Points {
	eventFor := func(facts agenthooks.Facts, point share.Point) share.Event {
		return share.Event{
			Point:   point,
			Session: share.SessionContext{ID: facts.Session.ID, Mode: facts.Session.Mode, Title: facts.Session.Title, Status: facts.Session.Status},
			Actor: share.ActorContext{
				Platform: facts.Actor.Platform, Role: string(facts.Actor.Role), GroupRole: string(facts.Actor.GroupRole),
				ID: facts.Actor.ID, Nickname: facts.Actor.Nickname,
				GroupCard: facts.Actor.GroupCard, Name: facts.Actor.Name,
			},
		}
	}
	message := func(value agenthooks.Message) share.MessagePayload {
		return share.MessagePayload{
			ID: value.ID, Role: value.Role, PlatformText: value.PlatformText,
			IntentText: value.IntentText, Segments: value.Segments,
		}
	}
	response := func(value agenthooks.LLMResponse) share.LLMPayload {
		return share.LLMPayload{
			Provider: value.Provider, Model: value.Model,
			Text: value.Text, SourceText: value.SourceText, Usage: value.Usage,
			ToolCalls: value.ToolCalls, ElapsedMS: value.ElapsedMS,
		}
	}
	input := func(point share.Point) func(context.Context, agenthooks.Facts, agenthooks.LLMInput) (agenthooks.LLMInputResult, error) {
		return func(ctx context.Context, facts agenthooks.Facts, value agenthooks.LLMInput) (agenthooks.LLMInputResult, error) {
			event := eventFor(facts, point)
			event.Message = message(value.Message)
			// Agent already supplies detached messages. Their read-only view is
			// never copied back into Agent; only this point's result fields return.
			event.LLM = share.LLMPayload{Provider: value.Provider, Model: value.Model, Messages: value.Messages, Tools: value.Tools}
			updated, err := m.run(ctx, event, m.callOptions(facts, agent))
			return agenthooks.LLMInputResult{Segments: updated.Message.Segments, Tools: updated.LLM.Tools}, err
		}
	}
	output := func(point share.Point) func(context.Context, agenthooks.Facts, string) (string, error) {
		return func(ctx context.Context, facts agenthooks.Facts, text string) (string, error) {
			event := eventFor(facts, point)
			event.Message = share.MessagePayload{Role: string(llm.RoleAssistant), Segments: llm.TextSegments(text)}
			updated, err := m.run(ctx, event, m.callOptions(facts, agent))
			return llm.SegmentsTextOnly(updated.Message.Segments), err
		}
	}
	return agenthooks.Points{
		HasRoute: func(ctx context.Context, facts agenthooks.Facts, value agenthooks.Message) bool {
			ctx, done, err := m.beginCall(ctx)
			if err != nil {
				return false
			}
			defer done()
			event := eventFor(facts, share.PointPlatformMessageReceived)
			event.Message = message(value)
			return m.runtime.RouteHookID(fillEventFromContext(ctx, event)) != ""
		},
		CancelRoute: func(ctx context.Context, facts agenthooks.Facts, value agenthooks.Message) bool {
			ctx, done, err := m.beginCall(ctx)
			if err != nil {
				return false
			}
			defer done()
			event := eventFor(facts, share.PointPlatformMessageReceived)
			event.Message = message(value)
			return m.runtime.Cancel(fillEventFromContext(ctx, event))
		},
		PlatformMessageReceived: func(ctx context.Context, facts agenthooks.Facts, value agenthooks.Message) (agenthooks.PlatformMessageReceivedResult, error) {
			event := eventFor(facts, share.PointPlatformMessageReceived)
			event.Message = message(value)
			updated, err := m.run(ctx, event, m.callOptions(facts, agent))
			return agenthooks.PlatformMessageReceivedResult{Segments: updated.Message.Segments, Consume: updated.Control.Consume, Outputs: updated.Outputs}, err
		},
		AgentInputPrepared: func(ctx context.Context, facts agenthooks.Facts, value agenthooks.Message) ([]llm.MessageSegment, error) {
			event := eventFor(facts, share.PointAgentInputPrepared)
			event.Message = message(value)
			updated, err := m.run(ctx, event, m.callOptions(facts, agent))
			return updated.Message.Segments, err
		},
		LLMTurnPrepared:    input(share.PointLLMTurnPrepared),
		LLMRequestPrepared: input(share.PointLLMRequestPrepared),
		LLMResponseReceived: func(ctx context.Context, facts agenthooks.Facts, value agenthooks.LLMResponse) (agenthooks.LLMResponseResult, error) {
			event := eventFor(facts, share.PointLLMResponseReceived)
			event.LLM = response(value)
			updated, err := m.run(ctx, event, m.callOptions(facts, agent))
			return agenthooks.LLMResponseResult{
				Text: updated.LLM.Text, SourceText: updated.LLM.SourceText,
				Usage: updated.LLM.Usage, ToolCalls: updated.LLM.ToolCalls, Outputs: updated.Outputs,
			}, err
		},
		ToolCallPrepared: func(ctx context.Context, facts agenthooks.Facts, value llm.ToolCallRequest) (string, error) {
			event := eventFor(facts, share.PointToolCallPrepared)
			event.Tool = share.ToolPayload{ID: value.ID, Name: value.Name, Arguments: value.Arguments}
			updated, err := m.run(ctx, event, m.callOptions(facts, agent))
			return updated.Tool.Arguments, err
		},
		ToolCallCompleted: func(ctx context.Context, facts agenthooks.Facts, value agenthooks.ToolResult) ([]llm.MessageSegment, error) {
			event := eventFor(facts, share.PointToolCallCompleted)
			original := append([]llm.MessageSegment(nil), value.Segments...)
			text := llm.SegmentsTextOnly(original)
			event.Message = share.MessagePayload{Role: string(llm.RoleTool), Segments: append([]llm.MessageSegment(nil), original...)}
			event.Tool = share.ToolPayload{ID: value.Call.ID, Name: value.Call.Name, Arguments: value.Call.Arguments, Risk: value.Risk, Result: text, Error: value.Err}
			updated, err := m.run(ctx, event, m.callOptions(facts, agent))
			if err != nil {
				return nil, err
			}
			if !reflect.DeepEqual(updated.Message.Segments, original) {
				return updated.Message.Segments, nil
			}
			if updated.Tool.Result != text {
				return llm.SetSegmentText(original, updated.Tool.Result), nil
			}
			return original, nil
		},
		AgentOutputPrepared:     output(share.PointAgentOutputPrepared),
		AgentTurnOutputPrepared: output(share.PointAgentTurnOutputPrepared),
		PlatformMessageSent: func(ctx context.Context, facts agenthooks.Facts, text string) {
			event := eventFor(facts, share.PointPlatformMessageSent)
			event.Message = share.MessagePayload{Role: string(llm.RoleAssistant), Segments: llm.TextSegments(text)}
			m.notify(ctx, event, m.callOptions(facts, agent))
		},
		ErrorOccurred: func(ctx context.Context, facts agenthooks.Facts, value agenthooks.LLMResponse, err error) {
			event := eventFor(facts, share.PointErrorOccurred)
			event.LLM, event.Error = response(value), err
			m.notify(ctx, event, m.callOptions(facts, agent))
		},
	}
}
