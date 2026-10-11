package chat

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"elbot/internal/agent/dialogue"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/llm/chatcompletions"
	"elbot/internal/modelmgr"
	notificationrules "elbot/internal/notification/rules"
	"elbot/internal/storage"
)

// Caller owns Chat request construction and stream consumption.
type Caller struct{ Calls *dialogue.CallProcessor }
type llmCallResult struct {
	Text      string
	RawText   string
	Usage     *llm.Usage
	ToolCalls []llm.ToolCallRequest
	Outputs   []delivery.Output
	Messages  []llm.LLMMessage
	Stream    delivery.MessageStream
}

func (c *Caller) Call(ctx context.Context, session *storage.Session, selection modelmgr.Selection, messages []llm.LLMMessage, tools []llm.ToolSchema, pending *dialogue.PendingUserMessage, stream delivery.MessageStream, out dialogue.Output) (result llmCallResult, callErr error) {
	client, ok := selection.Client.(chatcompletions.Streamer)
	if !ok {
		return llmCallResult{}, fmt.Errorf("provider %q does not provide the Chat streaming capability", selection.Provider)
	}
	sessionID := session.ID
	toolsEnabled := session.Mode == storage.SessionModeWork || session.Mode == storage.SessionModeBackground
	startedAt := time.Now()
	observation := c.Calls.Begin(sessionID, selection)
	completed := &observation.Event
	publishCompleted := func() { observation.Publish(ctx, callErr) }
	defer publishCompleted()
	input, cleanup, err := c.Calls.PrepareCall(ctx, session, selection, messages, tools, pending, &observation.LogRecord)
	if err != nil {
		return llmCallResult{}, err
	}
	defer cleanup()
	baseMessages, requestMessages, tools := input.Messages, input.RequestMessages, input.Tools
	req := chatcompletions.Request{
		Model:     selection.Model,
		SessionID: sessionID,
		Messages:  requestMessages,
		Tools:     tools,
	}
	observation.LogRecord.EventName = "llm_usage"
	ch, err := client.Stream(ctx, req)
	if err != nil {
		completed.Err = err
		observation.LogRecord.Content = fmt.Sprintf("call_error=%v", err)
		if errors.Is(err, context.Canceled) {
			observation.LogRecord.ResultStatus, observation.LogRecord.Level = events.ResultCanceled, slog.LevelInfo
		}
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return llmCallResult{Messages: baseMessages, Stream: stream}, nil
		}
		if shouldFallbackVision(requestMessages, err) {
			completed.Err, completed.VisionFallback = err, true
			publishCompleted()
			c.Calls.RecordVisionFallback(ctx, session)
			return c.Call(ctx, session, selection, fallbackVisionMessages(baseMessages), tools, nil, stream, out)
		}
		completed.ProviderError, completed.Err = true, err
		completed.ElapsedMS = elapsedMillis(startedAt)
		c.Calls.NotifyError(ctx, hook.Event{
			Point:   hook.PointLLMResponseReceived,
			Session: hook.SessionContext{ID: sessionID},
			LLM:     hook.LLMPayload{Provider: selection.Provider, Model: selection.Model, ElapsedMS: elapsedMillis(startedAt)},
		}, err)
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
			observation.LogRecord.Content = fmt.Sprintf("call_error=%v", chunk.Error)
			if errors.Is(chunk.Error, context.Canceled) {
				observation.LogRecord.ResultStatus, observation.LogRecord.Level = events.ResultCanceled, slog.LevelInfo
			}
			if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				content := assistant.String()
				return llmCallResult{Text: content, RawText: content, Usage: usage, ToolCalls: toolCalls, Messages: baseMessages, Stream: stream}, nil
			}
			if shouldFallbackVision(requestMessages, chunk.Error) {
				if reasoning != nil {
					reasoning.Finish(ctx)
				}
				completed.Err, completed.VisionFallback = chunk.Error, true
				publishCompleted()
				c.Calls.RecordVisionFallback(ctx, session)
				return c.Call(ctx, session, selection, fallbackVisionMessages(baseMessages), tools, nil, stream, out)
			}
			completed.ProviderError, completed.Err = true, chunk.Error
			completed.ElapsedMS = elapsedMillis(startedAt)
			c.Calls.NotifyError(ctx, hook.Event{
				Point:   hook.PointLLMResponseReceived,
				Session: hook.SessionContext{ID: sessionID},
				LLM: hook.LLMPayload{
					Provider:   selection.Provider,
					Model:      selection.Model,
					SourceText: assistant.String(),
					Text:       assistant.String(),
					ToolCalls:  toolCalls,
					Usage:      usage,
					ElapsedMS:  elapsedMillis(startedAt),
				},
			}, chunk.Error)
			out.Notify(context.WithoutCancel(ctx), slog.LevelError, notificationrules.ModelInterrupted(chunk.Error))

			return llmCallResult{}, dialogue.MarkUserNotified(fmt.Errorf("chat stream: %w", chunk.Error))
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
				observation.LogRecord.Content = fmt.Sprintf("stream_error=%v", err)
				if errors.Is(err, context.Canceled) {
					observation.LogRecord.ResultStatus, observation.LogRecord.Level = events.ResultCanceled, slog.LevelInfo
				}
				return llmCallResult{}, fmt.Errorf("stream append: %w", err)
			}
		}
	}
	if reasoning != nil {
		reasoning.Finish(ctx)
	}
	elapsedMs := elapsedMillis(startedAt)
	completed.ElapsedMS = elapsedMs
	content := assistant.String()
	completed.Text, completed.SourceText, completed.Usage, completed.ToolCallCount = content, content, usage, len(toolCalls)
	completed.OutputReady = assistant.Len() > 0 || len(toolCalls) > 0
	if err := ctx.Err(); err != nil {
		completed.Err = err
		observation.LogRecord.Content = fmt.Sprintf("call_error=%v", err)
		if errors.Is(err, context.Canceled) {
			observation.LogRecord.ResultStatus, observation.LogRecord.Level = events.ResultCanceled, slog.LevelInfo
		}
		return llmCallResult{Text: content, RawText: content, Usage: usage, ToolCalls: toolCalls, Messages: baseMessages, Stream: stream}, nil
	}
	final, err := c.Calls.CompleteCall(ctx, session, selection, content, usage, toolCalls, elapsedMs)
	if err != nil {
		observation.LogRecord.Content = fmt.Sprintf("response_hook_error=%v", err)
		if errors.Is(err, context.Canceled) {
			observation.LogRecord.ResultStatus, observation.LogRecord.Level = events.ResultCanceled, slog.LevelInfo
		}
		return llmCallResult{}, err
	}
	observation.LogRecord.ResultStatus, observation.LogRecord.Level, observation.LogRecord.EventName = events.ResultSucceeded, slog.LevelInfo, "llm_usage"
	completed.OutputReady = true
	completed.Text, completed.SourceText, completed.ToolCallCount, completed.Usage = final.Text, final.SourceText, len(final.ToolCalls), final.Usage
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
