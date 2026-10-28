package chat

import (
	"context"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/media"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/llm/chatcompletions"
	"elbot/internal/modelmgr"
	"elbot/internal/storage"
)

// Caller owns Chat request construction and stream consumption.
type Caller struct {
	Messages interface {
		Committer(string) agentshare.MessageCommitter
	}
	Preparer interface {
		PrepareMaterials(context.Context, []llm.LLMMessage) ([]llm.LLMMessage, media.UsageID, error)
		FinishMaterials(context.Context, media.UsageID)
	}
	Calls interface {
		Begin(string, modelmgr.Selection) agentshare.CallObservation
		PrepareCall(context.Context, *storage.Session, modelmgr.Selection, []llm.LLMMessage, []llm.ToolSchema, *agentshare.PendingUserMessage, agentshare.MessageCommitter, *events.LogRecord) (agentshare.CallInput, error)
		NotifyError(context.Context, string, agentshare.LLMResponse, error)
		CompleteCall(context.Context, *storage.Session, modelmgr.Selection, string, *llm.Usage, []llm.ToolCallRequest, int64) (agentshare.CallOutput, error)
		RecordVisionFallback(context.Context, *storage.Session)
	}
}
type llmCallResult struct {
	Text      string
	RawText   string
	Usage     *llm.Usage
	ToolCalls []llm.ToolCallRequest
	Outputs   []delivery.Output
	Messages  []llm.LLMMessage
	Stream    delivery.MessageStream
}

func (c *Caller) Call(ctx context.Context, session *storage.Session, selection modelmgr.Selection, messages []llm.LLMMessage, tools []llm.ToolSchema, pending *agentshare.PendingUserMessage, stream delivery.MessageStream, out agentshare.Output) (result llmCallResult, callErr error) {
	client := selection.Client.(chatcompletions.Streamer)
	sessionID := session.ID
	toolsEnabled := session.Mode == storage.SessionModeWork || session.Mode == storage.SessionModeBackground
	startedAt := time.Now()
	observation := c.Calls.Begin(sessionID, selection)
	completed := observation.Event()
	publishCompleted := func() { observation.Publish(ctx, callErr) }
	defer publishCompleted()
	input, err := c.Calls.PrepareCall(ctx, session, selection, messages, tools, pending, c.Messages.Committer("append_pending_user_message"), observation.LogRecord())
	if err != nil {
		return llmCallResult{}, err
	}
	requestMessages, usageID, err := c.Preparer.PrepareMaterials(ctx, input.RequestMessages)
	defer c.Preparer.FinishMaterials(ctx, usageID)
	if err != nil {
		return llmCallResult{}, err
	}
	baseMessages, tools := input.Messages, input.Tools
	req := chatcompletions.Request{
		Model:     selection.Model,
		SessionID: sessionID,
		Messages:  requestMessages,
		Tools:     tools,
	}
	observation.LogRecord().EventName = "llm_usage"
	observation.LogRecord().ResultStatus = events.ResultCanceled
	callCtx, cancel := context.WithCancel(ctx)
	ch, err := client.Stream(callCtx, req)
	defer func() {
		cancel()
		if ch != nil {
			for range ch {
			}
		}
	}()
	if err != nil {
		completed.Err = err
		observation.LogRecord().Content = fmt.Sprintf("call_error=%v", err)
		if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
			observation.LogRecord().ResultStatus, observation.LogRecord().Level = events.ResultCanceled, slog.LevelInfo
		}
		if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
			return llmCallResult{Messages: baseMessages, Stream: stream}, nil
		}
		if shouldFallbackVision(requestMessages, err) {
			observation.LogRecord().ResultStatus, observation.LogRecord().Level = events.ResultFailed, slog.LevelError
			completed.Err, completed.VisionFallback = err, true
			publishCompleted()
			c.Calls.RecordVisionFallback(ctx, session)
			return c.Call(ctx, session, selection, fallbackVisionMessages(baseMessages), tools, nil, stream, out)
		}
		completed.ProviderError, completed.Err = true, err
		observation.LogRecord().ResultStatus, observation.LogRecord().Level = events.ResultFailed, slog.LevelError
		completed.ElapsedMS = elapsedMillis(startedAt)
		publishCompleted()
		c.Calls.NotifyError(ctx, sessionID, agentshare.LLMResponse{Provider: selection.Provider, Model: selection.Model, ElapsedMS: elapsedMillis(startedAt)}, err)
		return llmCallResult{}, fmt.Errorf("chat: %w", err)
	}
	var assistant strings.Builder
	var usage *llm.Usage
	var toolCalls []llm.ToolCallRequest
	reasoning := out.StartReasoning(ctx)
	if reasoning != nil {
		defer reasoning.Finish(ctx)
	}
	for chunk := range ch {
		if chunk.Error != nil {
			completed.Err = chunk.Error
			completed.Text, completed.SourceText, completed.Usage, completed.ToolCallCount = assistant.String(), assistant.String(), usage, len(toolCalls)
			completed.OutputReady = assistant.Len() > 0 || len(toolCalls) > 0
			observation.LogRecord().Content = fmt.Sprintf("call_error=%v", chunk.Error)
			if ctx.Err() != nil && errors.Is(chunk.Error, ctx.Err()) {
				observation.LogRecord().ResultStatus, observation.LogRecord().Level = events.ResultCanceled, slog.LevelInfo
			}
			if ctx.Err() != nil && errors.Is(chunk.Error, ctx.Err()) {
				content := assistant.String()
				return llmCallResult{Text: content, RawText: content, Usage: usage, ToolCalls: toolCalls, Messages: baseMessages, Stream: stream}, nil
			}
			if shouldFallbackVision(requestMessages, chunk.Error) {
				observation.LogRecord().ResultStatus, observation.LogRecord().Level = events.ResultFailed, slog.LevelError
				if reasoning != nil {
					reasoning.Finish(ctx)
				}
				completed.Err, completed.VisionFallback = chunk.Error, true
				publishCompleted()
				c.Calls.RecordVisionFallback(ctx, session)
				return c.Call(ctx, session, selection, fallbackVisionMessages(baseMessages), tools, nil, stream, out)
			}
			completed.ProviderError, completed.Err = true, chunk.Error
			observation.LogRecord().ResultStatus, observation.LogRecord().Level = events.ResultFailed, slog.LevelError
			completed.ElapsedMS = elapsedMillis(startedAt)
			publishCompleted()
			c.Calls.NotifyError(ctx, sessionID, agentshare.LLMResponse{
				Provider:   selection.Provider,
				Model:      selection.Model,
				SourceText: assistant.String(),
				Text:       assistant.String(),
				ToolCalls:  toolCalls,
				Usage:      usage,
				ElapsedMS:  elapsedMillis(startedAt),
			}, chunk.Error)

			return llmCallResult{}, fmt.Errorf("chat stream: %w", chunk.Error)
		}
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
		if chunk.DeltaReasoningContent != "" && reasoning != nil {
			reasoning.Append(ctx, chunk.DeltaReasoningContent)
		}
		if toolsEnabled {
			for _, delta := range chunk.ToolCallDeltas {
				toolCalls = append(toolCalls, llm.ToolCallRequest{ID: delta.ID, Name: delta.Name, Arguments: delta.Args})
			}
		}
		delta := chunk.DeltaContent
		assistant.WriteString(delta)
		if stream != nil && delta != "" {
			if err := stream.Append(ctx, delta); err != nil {
				completed.Text, completed.SourceText, completed.Usage, completed.ToolCallCount = assistant.String(), assistant.String(), usage, len(toolCalls)
				completed.OutputReady = assistant.Len() > 0 || len(toolCalls) > 0
				observation.LogRecord().ResultStatus, observation.LogRecord().Level = events.ResultCanceled, slog.LevelInfo
				return llmCallResult{}, fmt.Errorf("stream append: %w", err)
			}
		}
	}
	elapsedMs := elapsedMillis(startedAt)
	completed.ElapsedMS = elapsedMs
	content := assistant.String()
	completed.Text, completed.SourceText, completed.Usage, completed.ToolCallCount = content, content, usage, len(toolCalls)
	completed.OutputReady = assistant.Len() > 0 || len(toolCalls) > 0
	if err := ctx.Err(); err != nil {
		completed.Err = err
		observation.LogRecord().ResultStatus, observation.LogRecord().Level = events.ResultCanceled, slog.LevelInfo
		return llmCallResult{Text: content, RawText: content, Usage: usage, ToolCalls: toolCalls, Messages: baseMessages, Stream: stream}, nil
	}
	observation.LogRecord().ResultStatus, observation.LogRecord().Level = events.ResultSucceeded, slog.LevelInfo
	publishCompleted()
	if reasoning != nil {
		reasoning.Finish(ctx)
	}
	final, err := c.Calls.CompleteCall(ctx, session, selection, content, usage, toolCalls, elapsedMs)
	if err != nil {
		return llmCallResult{}, err
	}
	return llmCallResult{Text: final.Text, RawText: content, Usage: final.Usage, ToolCalls: final.ToolCalls, Outputs: final.Outputs, Messages: baseMessages, Stream: stream}, nil
}

func shouldFallbackVision(messages []llm.LLMMessage, err error) bool {
	if err == nil || !llm.MessagesHaveImageSegment(messages) {
		return false
	}
	text := strings.ToLower(err.Error())
	needles := []string{"image", "vision", "multimodal", "content part", "unexpected item type in content", "provided messages input is invalid", "unsupported", "does not support"}
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

func fallbackVisionMessages(messages []llm.LLMMessage) []llm.LLMMessage {
	out := append([]llm.LLMMessage(nil), messages...)
	for i := range out {
		if len(out[i].Segments) == 0 {
			continue
		}
		out[i].Segments = llm.TextSegments(llm.SegmentsContentText(out[i].Segments))
	}
	return out
}

func elapsedMillis(startedAt time.Time) int64 {
	return time.Since(startedAt).Milliseconds()
}
