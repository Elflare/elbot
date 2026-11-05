package dialogue

import (
	"context"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/contextinfo"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"elbot/internal/elyph"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/storage"
)

type CallProcessor struct {
	// Status owns the latest raw provider usage, independently of response Hooks.
	Status interface {
		RecordUsage(context.Context, string, *llm.Usage)
	}
	Preparer *Preparer
	Hooks    interface {
		LLMRequestPrepared(context.Context, agentshare.Session, agentshare.LLMInput) (agentshare.LLMInputResult, error)
		LLMResponseReceived(context.Context, agentshare.Session, agentshare.LLMResponse) (agentshare.LLMResponseResult, error)
		ErrorOccurred(context.Context, agentshare.Session, agentshare.LLMResponse, error)
	}
	Tools *ToolExecutor
}
type CallObservation struct {
	processor *CallProcessor
	sessionID string
	startedAt time.Time
	published bool
	event     agentshare.ModelCallCompletedEvent
	// LogRecord holds this call's source-defined logging result independently of
	// the business event. Branches fill status and content; Publish does not infer
	// outcomes from Event.Err. Identity is fixed from the source at publication.
	logRecord events.LogRecord
}

// Call owns the shared preparation, retry and completion boundaries. Protocol
// callbacks retain their private request state and finish each attempt before returning.
func (c *CallProcessor) Call(
	ctx context.Context, session *storage.Session, selection modelmgr.Selection,
	messages []llm.LLMMessage, tools []llm.ToolSchema,
	pending *agentshare.PendingUserMessage, persistence agentshare.MessageCommitter,
	prepare func(context.Context, agentshare.CallInput) error,
	attempt func(context.Context, agentshare.CallObservation) (agentshare.CallOutput, error),
) (agentshare.CallOutput, error) {
	startedAt := time.Now()
	observation := c.Begin(session.ID, selection)
	input, err := c.PrepareCall(ctx, session, selection, messages, tools, pending, persistence, observation.LogRecord())
	if err != nil {
		observation.Publish(ctx, err)
		return agentshare.CallOutput{}, err
	}
	if err := prepare(ctx, input); err != nil {
		observation.Publish(ctx, err)
		return agentshare.CallOutput{}, err
	}

	result, err := llm.Retry(ctx, selection.Client, func(
		ctx context.Context,
		selection modelmgr.Selection,
	) (result agentshare.CallOutput, err error) {
		observation = c.Begin(session.ID, selection)
		defer func() { observation.Publish(ctx, err) }()
		return attempt(ctx, observation)
	}, selection)
	if err != nil {
		completed := observation.Event()
		if completed.ProviderError && !(ctx.Err() != nil && errors.Is(err, ctx.Err())) {
			c.NotifyError(ctx, session.ID, agentshare.LLMResponse{
				Provider: selection.Provider, Model: selection.Model,
				Text: completed.Text, SourceText: completed.SourceText,
				ToolCalls: result.ToolCalls, Usage: completed.Usage, ElapsedMS: completed.ElapsedMS,
			}, err)
		}
		return result, err
	}
	return c.CompleteCall(ctx, session, selection, result.SourceText, result.Usage, result.ToolCalls, time.Since(startedAt).Milliseconds())
}

func (c *CallProcessor) Begin(sessionID string, selected modelmgr.Selection) agentshare.CallObservation {
	return &CallObservation{
		processor: c,
		sessionID: sessionID,
		startedAt: time.Now(),
		event:     agentshare.ModelCallCompletedEvent{Provider: selected.Provider, Model: selected.Model, ElapsedMS: -1},
		logRecord: events.LogRecord{
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
	o.event.Execution, _ = contextinfo.ExecutionFromContext(ctx)
	o.event.Execution.SessionID = o.sessionID
	if o.event.ElapsedMS < 0 {
		o.event.ElapsedMS = time.Since(o.startedAt).Milliseconds()
	}
	o.event.Usage = agentshare.CloneUsage(o.event.Usage)
	if o.processor.Status != nil {
		o.processor.Status.RecordUsage(ctx, o.sessionID, o.event.Usage)
	}
	if o.event.Err == nil {
		o.event.Err = err
	}
	content := map[string]any{
		"provider":          o.event.Provider,
		"model":             o.event.Model,
		"elapsed_ms":        o.event.ElapsedMS,
		"prompt_tokens":     nil,
		"completion_tokens": nil,
		"total_tokens":      nil,
		"cache_hit_tokens":  nil,
		"cache_hit_percent": o.event.Usage.CacheHitPercent(),
	}
	if o.logRecord.Content != "" {
		content["detail"] = fmt.Sprintf("%.8192s", o.logRecord.Content)
	}
	if o.event.ProviderError && o.event.Err != nil {
		content["error"] = fmt.Sprintf("%.2048s", o.event.Err.Error())
	}
	if usage := o.event.Usage; usage != nil {
		content["prompt_tokens"], content["completion_tokens"], content["total_tokens"], content["cache_hit_tokens"] = usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens, usage.CacheHitTokens
	}
	data, _ := json.Marshal(content)
	o.logRecord.Content = string(data)
	o.logRecord.Identity = events.LogIdentityFromExecution(o.event.Execution)
	o.logRecord.Identity.SessionID = &o.sessionID
	if o.logRecord.EventName != "" {
		_ = events.EmitLog(context.WithoutCancel(ctx), o.logRecord)
	}
	if o.event.OutputReady {
		record := o.logRecord
		record.Category, record.Type, record.EventName = events.LogRuntime, events.ContentJSON, "assistant_message"
		content := map[string]any{
			"provider": o.event.Provider, "model": o.event.Model, "elapsed_ms": o.event.ElapsedMS,
			"text": agentshare.TruncateLogContent(o.event.Text, 512), "tool_call_count": o.event.ToolCallCount,
		}
		if o.event.Text != o.event.SourceText {
			content["raw_text"] = agentshare.TruncateLogContent(o.event.SourceText, 512)
		}
		data, _ := json.Marshal(content)
		record.Content = string(data)
		_ = events.EmitLog(context.WithoutCancel(ctx), record)
	}
}
func (c *CallProcessor) NotifyError(ctx context.Context, sessionID string, response agentshare.LLMResponse, err error) {
	c.Hooks.ErrorOccurred(ctx, agentshare.Session{ID: sessionID}, response, err)
}

// PrepareCall applies common hooks and authorization to canonical business
// material. PrepareMaterials expands only the material selected for this call.
func (c *CallProcessor) PrepareCall(ctx context.Context, session *storage.Session, selection modelmgr.Selection, messages []llm.LLMMessage, tools []llm.ToolSchema, pending *agentshare.PendingUserMessage, persistence agentshare.MessageCommitter, record *events.LogRecord) (agentshare.CallInput, error) {
	sessionID := session.ID
	toolsEnabled := session.Mode == storage.SessionModeWork || session.Mode == storage.SessionModeBackground
	if !toolsEnabled {
		tools = nil
	}
	var allowedTools map[string]bool
	if session.Mode == storage.SessionModeBackground {
		discovered, err := c.Tools.Manager.DiscoveredToolsForSession(ctx, session.ID, true)
		if err != nil {
			return agentshare.CallInput{}, err
		}
		allowedTools = map[string]bool{}
		for _, item := range discovered {
			if item.Schema.Name != "" {
				allowedTools[item.Schema.Name] = true
			}
		}
	}
	baseMessages := llm.CloneMessages(messages)
	requestMessages := llm.CloneMessages(baseMessages)
	if notice, ok := agentshare.ForegroundNotice(session); ok {
		requestMessages = append(requestMessages, notice)
	}
	hookMessage := agentshare.Message{}
	if pending != nil {
		if err := storage.PrepareMessageID(ctx, c.Preparer.NewMessageID, &pending.Message); err != nil {
			return agentshare.CallInput{}, err
		}
		hookMessage = agentshare.Message{
			ID:           pending.Message.ID,
			Role:         string(llm.RoleUser),
			PlatformText: pending.PlatformText,
			Segments:     append([]llm.MessageSegment(nil), baseMessages[pending.MessageIndex].Segments...),
		}
	}
	result, err := c.Hooks.LLMRequestPrepared(ctx, agentshare.Session{ID: sessionID}, agentshare.LLMInput{
		Message:  hookMessage,
		Provider: selection.Provider,
		Model:    selection.Model,
		Messages: llm.CloneMessages(requestMessages),
		Tools:    tools,
	})
	if err != nil {
		record.Content += "\nrequest_hook:\n" + err.Error()
		record.ResultStatus, record.Level = events.ResultFailed, slog.LevelWarn
		if events.LogStatus(err) == events.ResultCanceled {
			record.ResultStatus, record.Level = events.ResultCanceled, slog.LevelInfo
		}
		if pending != nil {
			if persistErr := persistence.Commit(ctx, &pending.Message, nil); persistErr != nil {
				record.Content += "\npersistence:\n" + persistErr.Error()
				record.ResultStatus, record.Level = events.ResultFailed, slog.LevelError
				err = errors.Join(err, persistErr)
			}
		}
		return agentshare.CallInput{}, fmt.Errorf("llm request hook: %w", err)
	}
	tools = result.Tools
	// Hooks may change schemas, but cannot override registered availability.
	tools, err = c.Tools.Manager.FilterSchemas(ctx, tools)
	if err != nil {
		return agentshare.CallInput{}, fmt.Errorf("filter tool schemas: %w", err)
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
		archived, err := c.Preparer.ArchiveMaterials(ctx, []llm.LLMMessage{{Segments: result.Segments}})
		if err != nil {
			return agentshare.CallInput{}, err
		}
		segments := archived[0].Segments
		state, err := c.Tools.Manager.DecodeState(session.Metadata)
		if err != nil {
			return agentshare.CallInput{}, err
		}
		ruleCard := elyph.RuleCardIfNeeded(pending.NeedsElyphRules, state.ElyphRulesShown)
		if ruleCard != "" {
			segments = append(llm.TextSegments(ruleCard+"\n\n"), segments...)
			fields, err := storage.DecodeSessionMetadata(pending.Message.Metadata)
			if err != nil {
				return agentshare.CallInput{}, err
			}
			_ = fields.Set("elyph_rules_shown", true)
			pending.Message.Metadata, _ = fields.Encode()
		}
		baseMessages[pending.MessageIndex].Segments = segments
		requestMessages[pending.MessageIndex].Segments = segments
		pending.Message.Content = agentshare.MessageContent(segments)
		pending.Message.Segments = agentshare.StoredMessageSegments(segments)
		if err := persistence.Commit(ctx, &pending.Message, nil); err != nil {
			return agentshare.CallInput{}, err
		}
		if ruleCard != "" {
			fields, _ := storage.DecodeSessionMetadata(session.Metadata)
			_ = fields.Set("elyph_rules_shown", true)
			session.Metadata, _ = fields.Encode()
		}
	}
	return agentshare.CallInput{Messages: baseMessages, RequestMessages: requestMessages, Tools: tools}, nil
}

func (c *CallProcessor) CompleteCall(ctx context.Context, session *storage.Session, selection modelmgr.Selection, content string, usage *llm.Usage, toolCalls []llm.ToolCallRequest, elapsedMs int64) (agentshare.CallOutput, error) {
	sessionID := session.ID
	toolsEnabled := session.Mode == storage.SessionModeWork || session.Mode == storage.SessionModeBackground
	result, err := c.Hooks.LLMResponseReceived(ctx, agentshare.Session{ID: sessionID}, agentshare.LLMResponse{
		Provider:   selection.Provider,
		Model:      selection.Model,
		Usage:      usage,
		SourceText: content,
		Text:       content,
		ToolCalls:  append([]llm.ToolCallRequest(nil), toolCalls...),
		ElapsedMS:  elapsedMs,
	})
	if err != nil {
		return agentshare.CallOutput{}, fmt.Errorf("llm response hook: %w", err)
	}
	usage = result.Usage
	toolCalls = result.ToolCalls
	if !toolsEnabled {
		toolCalls = nil
	}
	return agentshare.CallOutput{Text: result.Text, SourceText: result.SourceText, Usage: usage, ToolCalls: toolCalls, Outputs: result.Outputs}, nil
}

func (o *CallObservation) Event() *agentshare.ModelCallCompletedEvent { return &o.event }
func (o *CallObservation) LogRecord() *events.LogRecord               { return &o.logRecord }
