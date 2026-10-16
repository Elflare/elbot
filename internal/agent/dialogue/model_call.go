package dialogue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	agentevents "elbot/internal/agent/events"
	"elbot/internal/delivery"
	"elbot/internal/elyph"
	"elbot/internal/events"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	sessionpkg "elbot/internal/session"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/toolrun"
)

type CallProcessor struct {
	// Status owns the latest raw provider usage, independently of response Hooks.
	Status interface {
		RecordUsage(context.Context, string, *llm.Usage)
	}
	Messages  *MessageStore
	Preparer  *Preparer
	Hooks     Hooks
	Identity  Identity
	Tools     *ToolExecutor
	Completed *signal.Signal[agentevents.ModelCallCompletedEvent, signal.Empty]
	Vision    *signal.Signal[agentevents.VisionFallbackUsedEvent, signal.Empty]
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
	// LogRecord holds this call's source-defined logging result independently of
	// the business event. Branches fill status and content; Publish does not infer
	// outcomes from Event.Err. Identity is fixed from the source at publication.
	LogRecord events.LogRecord
}

func (c *CallProcessor) Begin(sessionID string, selected modelmgr.Selection) *CallObservation {
	return &CallObservation{
		processor: c,
		sessionID: sessionID,
		startedAt: time.Now(),
		Event:     agentevents.ModelCallCompletedEvent{Provider: selected.Provider, Model: selected.Model, ElapsedMS: -1},
		LogRecord: events.LogRecord{
			Category:     events.LogAudit,
			Level:        slog.LevelInfo,
			Module:       "agent",
			Type:         events.ContentJSON,
			ResultStatus: events.ResultSkipped,
		},
	}
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
	if o.processor.Status != nil {
		o.processor.Status.RecordUsage(ctx, o.sessionID, o.Event.Usage)
	}
	if o.Event.Err == nil {
		o.Event.Err = err
	}
	content := map[string]any{
		"provider":          o.Event.Provider,
		"model":             o.Event.Model,
		"elapsed_ms":        o.Event.ElapsedMS,
		"prompt_tokens":     nil,
		"completion_tokens": nil,
		"total_tokens":      nil,
		"cache_hit_tokens":  nil,
	}
	if o.Event.VisionFallback {
		content["vision_fallback"] = true
	}
	if o.LogRecord.Content != "" {
		content["detail"] = fmt.Sprintf("%.8192s", o.LogRecord.Content)
	}
	if o.Event.ProviderError && o.Event.Err != nil {
		content["error"] = fmt.Sprintf("%.2048s", o.Event.Err.Error())
	}
	if usage := o.Event.Usage; usage != nil {
		content["prompt_tokens"], content["completion_tokens"], content["total_tokens"], content["cache_hit_tokens"] = usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens, usage.CacheHitTokens
	}
	data, _ := json.Marshal(content)
	o.LogRecord.Content = string(data)
	o.LogRecord.Identity.SessionID = &o.sessionID
	if o.Event.RunID != "" {
		o.LogRecord.Identity.RunID = &o.Event.RunID
	}
	if o.Event.Attempt != "" {
		o.LogRecord.Identity.Attempt = &o.Event.Attempt
	}
	if o.Event.RequestID != "" {
		o.LogRecord.Identity.RequestID = &o.Event.RequestID
	}
	if o.Event.RootRequestID != "" {
		o.LogRecord.Identity.RootRequestID = &o.Event.RootRequestID
	}
	if o.LogRecord.EventName != "" {
		_ = events.EmitLog(context.WithoutCancel(ctx), o.LogRecord)
	}
	if o.Event.OutputReady {
		record := o.LogRecord
		record.Category, record.Type, record.EventName = events.LogRuntime, events.ContentText, "assistant_message"
		record.ContentLimit = 4096
		record.Content = fmt.Sprintf("provider=%s\nmodel=%s\nelapsed_ms=%d\ntext=%s", o.Event.Provider, o.Event.Model, o.Event.ElapsedMS, o.Event.Text)
		if o.Event.Text != o.Event.SourceText {
			record.Content += "\nraw_text=" + o.Event.SourceText
		}
		record.Content += fmt.Sprintf("\ntool_call_count=%d", o.Event.ToolCallCount)
		record.Content = tool.TruncateLogContent(record.Content, 512)
		_ = events.EmitLog(context.WithoutCancel(ctx), record)
	}
	agentevents.Emit(ctx, o.processor.Completed, o.Event)
}
func (c *CallProcessor) RecordVisionFallback(ctx context.Context, row *storage.Session) {
	agentevents.Emit((ExecutionView{}).Context(ctx), c.Vision, agentevents.VisionFallbackUsedEvent{EventMeta: agentevents.Meta(ctx, row.ID), Visible: c.Identity.IsCLI(ctx) && !sessionpkg.IsBackground(row)})
}
func (c *CallProcessor) NotifyError(ctx context.Context, event hook.Event, err error) {
	event.Point = hook.PointErrorOccurred
	event.Error = err
	c.Hooks.Notify(ctx, event)
}

// PrepareCall applies common hooks and authorization to canonical business
// material. PrepareMaterials expands only the material selected for this call.
func (c *CallProcessor) PrepareCall(ctx context.Context, session *storage.Session, selection modelmgr.Selection, messages []llm.LLMMessage, tools []llm.ToolSchema, pending *PendingUserMessage, persistence MessageCommitter, record *events.LogRecord) (CallInput, error) {
	sessionID := session.ID
	toolsEnabled := session.Mode == storage.SessionModeWork || session.Mode == storage.SessionModeBackground
	if !toolsEnabled {
		tools = nil
	}
	var allowedTools map[string]bool
	if session.Mode == storage.SessionModeBackground {
		cached, _, err := CachedToolsForSession(ctx, c.Tools.State, c.Tools.Registry, session)
		if err != nil {
			return CallInput{}, err
		}
		allowedTools = toolrun.BackgroundToolNames(ctx, cached)
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
	event, logResult, err := c.Hooks.Run(ctx, hook.Event{
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
		record.Content += "\nrequest_hook:\n" + logResult.Content
		record.ResultStatus, record.Level = logResult.ResultStatus, slog.LevelInfo
		if logResult.ResultStatus == events.ResultFailed {
			record.Level = slog.LevelError
		}
		if pending != nil {
			if logResult, persistErr := persistence.Commit(ctx, &pending.Message, nil); persistErr != nil {
				record.Content += "\npersistence:\n" + logResult.Content
				record.ResultStatus, record.Level = events.ResultFailed, slog.LevelError
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
		archived, err := c.Preparer.ArchiveMaterials(ctx, []llm.LLMMessage{{Segments: event.Message.Segments}})
		if err != nil {
			return CallInput{}, err
		}
		segments := archived[0].Segments
		state, err := toolrun.DecodeState(session.Metadata)
		if err != nil {
			return CallInput{}, err
		}
		ruleCard := elyph.RuleCardIfNeeded(pending.NeedsElyphRules, state.ElyphRulesShown)
		if ruleCard != "" {
			segments = append(llm.TextSegments(ruleCard+"\n\n"), segments...)
			fields, err := storage.DecodeSessionMetadata(pending.Message.Metadata)
			if err != nil {
				return CallInput{}, err
			}
			_ = fields.Set("elyph_rules_shown", true)
			pending.Message.Metadata, _ = fields.Encode()
		}
		baseMessages[pending.MessageIndex].Segments = segments
		requestMessages[pending.MessageIndex].Segments = segments
		pending.Message.Content = MessageContent(segments)
		pending.Message.Segments = StoredMessageSegments(segments)
		if _, err := persistence.Commit(ctx, &pending.Message, nil); err != nil {
			return CallInput{}, err
		}
		if ruleCard != "" {
			fields, _ := storage.DecodeSessionMetadata(session.Metadata)
			_ = fields.Set("elyph_rules_shown", true)
			session.Metadata, _ = fields.Encode()
		}
	}
	return CallInput{Messages: baseMessages, RequestMessages: requestMessages, Tools: tools}, nil
}

func (c *CallProcessor) CompleteCall(ctx context.Context, session *storage.Session, selection modelmgr.Selection, content string, usage *llm.Usage, toolCalls []llm.ToolCallRequest, elapsedMs int64) (CallOutput, error) {
	sessionID := session.ID
	toolsEnabled := session.Mode == storage.SessionModeWork || session.Mode == storage.SessionModeBackground
	event, _, err := c.Hooks.Run(ctx, hook.Event{
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
