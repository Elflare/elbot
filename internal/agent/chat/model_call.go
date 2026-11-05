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
		Call(context.Context, *storage.Session, modelmgr.Selection, []llm.LLMMessage, []llm.ToolSchema, *agentshare.PendingUserMessage, agentshare.MessageCommitter,
			func(context.Context, agentshare.CallInput) error,
			func(context.Context, agentshare.CallObservation) (agentshare.CallOutput, error)) (agentshare.CallOutput, error)
	}
}
type llmCallResult struct {
	Text      string
	RawText   string
	Usage     *llm.Usage
	ToolCalls []llm.ToolCallRequest
	Outputs   []delivery.Output
	Messages  []llm.LLMMessage
}

func (c *Caller) Call(ctx context.Context, session *storage.Session, selection modelmgr.Selection, messages []llm.LLMMessage, tools []llm.ToolSchema, pending *agentshare.PendingUserMessage, stream delivery.MessageStream) (result llmCallResult, callErr error) {
	client := selection.Client.(chatcompletions.Streamer)
	toolsEnabled := session.Mode == storage.SessionModeWork || session.Mode == storage.SessionModeBackground
	var req chatcompletions.Request
	var usageID media.UsageID
	var baseMessages []llm.LLMMessage
	defer func() { c.Preparer.FinishMaterials(ctx, usageID) }()
	prepare := func(ctx context.Context, input agentshare.CallInput) error {
		baseMessages = input.Messages
		requestMessages, id, err := c.Preparer.PrepareMaterials(ctx, input.RequestMessages)
		usageID = id
		if err != nil {
			return err
		}
		req = chatcompletions.Request{Model: selection.Model, SessionID: session.ID, Messages: requestMessages, Tools: input.Tools}
		return nil
	}
	attempt := func(ctx context.Context, observation agentshare.CallObservation) (agentshare.CallOutput, error) {
		var err error
		result, err = c.callOnce(ctx, client, req, observation, stream, toolsEnabled)
		return agentshare.CallOutput{Text: result.RawText, SourceText: result.RawText, Usage: result.Usage, ToolCalls: result.ToolCalls}, err
	}
	final, err := c.Calls.Call(ctx, session, selection, messages, tools, pending, c.Messages.Committer("append_pending_user_message"), prepare, attempt)
	result.Messages = baseMessages
	if err != nil {
		if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
			return result, nil
		}
		return llmCallResult{}, err
	}
	result.Text, result.Usage, result.ToolCalls, result.Outputs = final.Text, final.Usage, final.ToolCalls, final.Outputs
	return result, nil
}

func (c *Caller) callOnce(ctx context.Context, client chatcompletions.Streamer, req chatcompletions.Request, observation agentshare.CallObservation, stream delivery.MessageStream, toolsEnabled bool) (result llmCallResult, callErr error) {
	startedAt := time.Now()
	completed := observation.Event()
	defer func() {
		if ctx.Err() != nil {
			_, _ = stream.Send(context.WithoutCancel(ctx), delivery.StreamEvent{Type: delivery.StreamReasoningDone})
			return
		}
		var failure *llm.CallError
		if errors.As(completed.Err, &failure) && (failure.Kind == llm.ErrorStreamFailure || failure.Kind == llm.ErrorPreviousResponseUnavailable) {
			if _, resetErr := stream.Send(ctx, delivery.StreamEvent{Type: delivery.StreamReset}); resetErr != nil {
				slog.ErrorContext(ctx, "reset message stream", "error", resetErr, "request_error", completed.Err)
				callErr = resetErr
			}
		}
	}()
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
		observation.LogRecord().ResultStatus = events.LogStatus(err)
		observation.LogRecord().Level = slog.LevelWarn
		if observation.LogRecord().ResultStatus == events.ResultCanceled {
			observation.LogRecord().Level = slog.LevelInfo
		}
		if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
			return llmCallResult{}, err
		}
		completed.ProviderError, completed.Err = true, err
		observation.LogRecord().ResultStatus, observation.LogRecord().Level = events.ResultFailed, slog.LevelWarn
		completed.ElapsedMS = elapsedMillis(startedAt)
		return llmCallResult{}, err
	}
	var assistant strings.Builder
	var usage *llm.Usage
	var toolCalls []llm.ToolCallRequest
	for chunk := range ch {
		if chunk.Error != nil {
			completed.Err = chunk.Error
			completed.Text, completed.SourceText, completed.Usage, completed.ToolCallCount = assistant.String(), assistant.String(), usage, len(toolCalls)
			completed.OutputReady = assistant.Len() > 0 || len(toolCalls) > 0
			observation.LogRecord().Content = fmt.Sprintf("call_error=%v", chunk.Error)
			observation.LogRecord().ResultStatus = events.LogStatus(chunk.Error)
			observation.LogRecord().Level = slog.LevelWarn
			if observation.LogRecord().ResultStatus == events.ResultCanceled {
				observation.LogRecord().Level = slog.LevelInfo
			}
			if ctx.Err() != nil && errors.Is(chunk.Error, ctx.Err()) {
				content := assistant.String()
				return llmCallResult{Text: content, RawText: content, Usage: usage, ToolCalls: toolCalls}, chunk.Error
			}
			completed.ProviderError, completed.Err = true, chunk.Error
			observation.LogRecord().ResultStatus, observation.LogRecord().Level = events.ResultFailed, slog.LevelWarn
			completed.ElapsedMS = elapsedMillis(startedAt)
			return llmCallResult{Text: assistant.String(), RawText: assistant.String(), Usage: usage, ToolCalls: toolCalls}, chunk.Error
		}
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
		if chunk.DeltaReasoningContent != "" {
			_, _ = stream.Send(ctx, delivery.StreamEvent{Type: delivery.StreamReasoningDelta, Text: chunk.DeltaReasoningContent})
		}
		if toolsEnabled {
			for _, delta := range chunk.ToolCallDeltas {
				toolCalls = append(toolCalls, llm.ToolCallRequest{ID: delta.ID, Name: delta.Name, Arguments: delta.Args})
			}
		}
		delta := chunk.DeltaContent
		assistant.WriteString(delta)
		if delta != "" {
			if _, err := stream.Send(ctx, delivery.StreamEvent{Type: delivery.StreamTextDelta, Text: delta}); err != nil {
				completed.Text, completed.SourceText, completed.Usage, completed.ToolCallCount = assistant.String(), assistant.String(), usage, len(toolCalls)
				completed.OutputReady = assistant.Len() > 0 || len(toolCalls) > 0
				observation.LogRecord().ResultStatus, observation.LogRecord().Level = events.ResultCanceled, slog.LevelInfo
				return llmCallResult{}, err
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
		return llmCallResult{Text: content, RawText: content, Usage: usage, ToolCalls: toolCalls}, err
	}
	observation.LogRecord().ResultStatus, observation.LogRecord().Level = events.ResultSucceeded, slog.LevelInfo
	_, err = stream.Send(ctx, delivery.StreamEvent{Type: delivery.StreamFinish})
	return llmCallResult{Text: content, RawText: content, Usage: usage, ToolCalls: toolCalls}, err
}

func elapsedMillis(startedAt time.Time) int64 {
	return time.Since(startedAt).Milliseconds()
}
