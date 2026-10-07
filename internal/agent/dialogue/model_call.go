package dialogue

import (
	"context"
	"errors"
	"fmt"
	"time"

	agentevents "elbot/internal/agent/events"
	"elbot/internal/delivery"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/modelmgr"
	"elbot/internal/session"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/toolrun"
)

type CallProcessor struct {
	Messages  *MessageStore
	Media     *media.Manager
	Hooks     Hooks
	Identity  Identity
	Tools     *ToolExecutor
	Completed *signal.Signal[agentevents.ModelCallCompletedEvent]
	Vision    *signal.Signal[agentevents.VisionFallbackUsedEvent]
}
type CallInput struct {
	Messages, RequestMessages []llm.LLMMessage
	Tools                     []llm.ToolSchema
}
type CallOutput struct {
	Text, SourceText string
	Usage            *llm.Usage
	ToolCalls        []llm.ToolCallRequest
	Outputs          []delivery.Output
}
type CallObservation struct {
	processor *CallProcessor
	sessionID string
	startedAt time.Time
	published bool
	Event     agentevents.ModelCallCompletedEvent
}

func (c *CallProcessor) Begin(sessionID string, selected modelmgr.Selection) *CallObservation {
	return &CallObservation{processor: c, sessionID: sessionID, startedAt: time.Now(), Event: agentevents.ModelCallCompletedEvent{Provider: selected.Provider, Model: selected.Model, ElapsedMS: -1}}
}
func (o *CallObservation) Publish(ctx context.Context, err error) {
	if o.published {
		return
	}
	o.published = true
	o.Event.EventMeta = agentevents.Meta(ctx, o.sessionID)
	if o.Event.ElapsedMS < 0 {
		o.Event.ElapsedMS = time.Since(o.startedAt).Milliseconds()
	}
	o.Event.Usage = agentevents.CloneUsage(o.Event.Usage)
	if o.Event.Err == nil {
		o.Event.Err = err
	}
	agentevents.Emit(ctx, o.processor.Completed, o.Event)
}
func (c *CallProcessor) RecordVisionFallback(ctx context.Context, row *storage.Session) {
	agentevents.Emit((ExecutionView{}).Context(ctx), c.Vision, agentevents.VisionFallbackUsedEvent{EventMeta: agentevents.Meta(ctx, row.ID), Visible: c.Identity.IsCLI(ctx) && !session.IsBackground(row)})
}
func (c *CallProcessor) NotifyError(ctx context.Context, event hook.Event, err error) {
	event.Point = hook.PointErrorOccurred
	event.Error = err
	c.Hooks.Notify(ctx, event)
}
func (c *CallProcessor) PrepareCall(ctx context.Context, session *storage.Session, selection modelmgr.Selection, messages []llm.LLMMessage, tools []llm.ToolSchema, pending *PendingUserMessage) (CallInput, func(), error) {
	input, err := c.PrepareProjection(ctx, session, selection, messages, tools, pending, c.Messages.Committer("append_pending_user_message"))
	if err != nil {
		return CallInput{}, func() {}, err
	}
	cleanup := func() {}
	if c.Media != nil {
		input.RequestMessages, cleanup, err = c.Media.AcquireForLLM(ctx, input.RequestMessages)
	}
	return input, cleanup, err
}

// PrepareProjection applies common hooks and authorization to business views.
// Native routes acquire only their new wire input, not old display history.
func (c *CallProcessor) PrepareProjection(ctx context.Context, session *storage.Session, selection modelmgr.Selection, messages []llm.LLMMessage, tools []llm.ToolSchema, pending *PendingUserMessage, persistence MessageCommitter) (CallInput, error) {
	sessionID := session.ID
	toolsEnabled := session.Mode == storage.SessionModeWork || session.Mode == storage.SessionModeBackground
	if !toolsEnabled {
		tools = nil
	}
	var allowedTools map[string]bool
	if session.Mode == storage.SessionModeBackground {
		cached, err := CachedToolsForSession(ctx, c.Tools.State, c.Tools.Registry, session)
		if err != nil {
			return CallInput{}, err
		}
		allowedTools = toolrun.BackgroundToolNames(ctx, cached, c.Tools.Registry)
	}
	baseMessages := llm.CloneMessages(messages)
	requestMessages := llm.CloneMessages(baseMessages)
	if notice, ok := ForegroundNotice(session); ok {
		requestMessages = append(requestMessages, notice)
	}
	hookMessage := hook.MessagePayload{}
	if pending != nil {
		hookMessage = hook.MessagePayload{
			ID:           pending.Message.ID,
			Role:         string(llm.RoleUser),
			PlatformText: pending.PlatformText,
			Segments:     append([]llm.MessageSegment(nil), baseMessages[pending.MessageIndex].Segments...),
		}
	}
	event, err := c.Hooks.Run(ctx, hook.Event{
		Point:   hook.PointLLMRequestPrepared,
		Session: hook.SessionContext{ID: sessionID},
		Message: hookMessage,
		LLM: hook.LLMPayload{
			Provider: selection.Provider,
			Model:    selection.Model,
			Messages: llm.CloneMessages(requestMessages),
			Tools:    tools,
		},
	})
	if err != nil {
		if pending != nil {
			if persistErr := persistence.Commit(ctx, &pending.Message); persistErr != nil {
				err = errors.Join(err, persistErr)
			}
		}
		return CallInput{}, fmt.Errorf("llm request hook: %w", err)
	}
	tools = event.LLM.Tools
	// Hooks may change schemas, but cannot override registered availability.
	if c.Tools != nil && c.Tools.Registry != nil {
		filtered := make([]llm.ToolSchema, 0, len(tools))
		for _, schema := range tools {
			if candidate, ok := c.Tools.Registry.Get(schema.Name); ok && !tool.InfoAvailableInContext(ctx, candidate.Info()) {
				continue
			}
			filtered = append(filtered, schema)
		}
		tools = filtered
	}
	if allowedTools != nil {
		filtered := make([]llm.ToolSchema, 0, len(tools))
		for _, schema := range tools {
			if allowedTools[schema.Name] {
				filtered = append(filtered, schema)
			}
		}
		tools = filtered
	}
	// Hooks may customize tools in work mode, but cannot enable them in chat.
	if !toolsEnabled {
		tools = nil
	}
	if pending != nil {
		segments := materialize(ctx, c.Media, event.Message.Segments)
		baseMessages[pending.MessageIndex].Segments = segments
		requestMessages[pending.MessageIndex].Segments = segments
		pending.Message.Content = llm.SegmentsContentText(segments)
		pending.Message.Segments = StoredMessageSegments(segments)
		if err := persistence.Commit(ctx, &pending.Message); err != nil {
			return CallInput{}, err
		}
	}
	return CallInput{Messages: baseMessages, RequestMessages: requestMessages, Tools: tools}, nil
}

func (c *CallProcessor) CompleteCall(ctx context.Context, session *storage.Session, selection modelmgr.Selection, content string, usage *llm.Usage, toolCalls []llm.ToolCallRequest, elapsedMs int64) (CallOutput, error) {
	sessionID := session.ID
	toolsEnabled := session.Mode == storage.SessionModeWork || session.Mode == storage.SessionModeBackground
	event, err := c.Hooks.Run(ctx, hook.Event{
		Point:   hook.PointLLMResponseReceived,
		Session: hook.SessionContext{ID: sessionID},
		LLM: hook.LLMPayload{
			Provider:   selection.Provider,
			Model:      selection.Model,
			Usage:      usage,
			SourceText: content,
			Text:       content,
			ToolCalls:  toolCalls,
			ElapsedMS:  elapsedMs,
		},
	})
	if err != nil {
		return CallOutput{}, fmt.Errorf("llm response hook: %w", err)
	}
	usage = event.LLM.Usage
	toolCalls = event.LLM.ToolCalls
	if !toolsEnabled {
		toolCalls = nil
	}
	finalText := event.LLM.Text
	return CallOutput{Text: finalText, SourceText: event.LLM.SourceText, Usage: usage, ToolCalls: toolCalls, Outputs: event.Outputs}, nil
}
