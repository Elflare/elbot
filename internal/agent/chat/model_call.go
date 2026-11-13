package chat

import (
	"context"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/media"
	"elbot/internal/operationresult"
	"errors"
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
		PrepareMaterials(context.Context, []llm.LLMMessage) (operationresult.Result[[]llm.LLMMessage], media.UsageID, error)
		FinishMaterials(context.Context, media.UsageID)
	}
	Calls interface {
		Call(context.Context, *storage.Session, modelmgr.Selection, []llm.LLMMessage, []llm.ToolSchema, *agentshare.PendingUserMessage, agentshare.MessageCommitter, delivery.MessageStream,
			func(context.Context, agentshare.CallInput) (operationresult.Result[struct{}], error),
			func(context.Context, agentshare.CallObservation) (operationresult.Result[agentshare.CallOutput], error)) (operationresult.Result[agentshare.CallOutput], error)
	}
}
type llmCallResult struct {
	Text             string
	RawText          string
	ReasoningContent string
	Usage            *llm.Usage
	ToolCalls        []llm.ToolCallRequest
	Outputs          []delivery.Output
	Messages         []llm.LLMMessage
}

func (c *Caller) Call(ctx context.Context, session *storage.Session, selection modelmgr.Selection, messages []llm.LLMMessage, tools []llm.ToolSchema, pending *agentshare.PendingUserMessage, stream delivery.MessageStream) (operationresult.Result[llmCallResult], error) {
	client := selection.Client.(chatcompletions.Streamer)
	toolsEnabled := session.Mode == storage.SessionModeWork || session.Mode == storage.SessionModeBackground
	var req chatcompletions.Request
	var usageID media.UsageID
	var baseMessages []llm.LLMMessage
	var result llmCallResult
	defer func() { c.Preparer.FinishMaterials(ctx, usageID) }()
	prepare := func(ctx context.Context, input agentshare.CallInput) (operationresult.Result[struct{}], error) {
		baseMessages = input.Messages
		requestMessages, id, err := c.Preparer.PrepareMaterials(ctx, input.RequestMessages)
		usageID = id
		if err != nil {
			return operationresult.Result[struct{}]{}, err
		}
		if requestMessages.Status != operationresult.Success {
			return operationresult.Result[struct{}]{Status: requestMessages.Status, Message: requestMessages.Message}, nil
		}
		req = chatcompletions.Request{Model: selection.Model, SessionID: session.ID, Messages: requestMessages.Data, Tools: input.Tools}
		return operationresult.Result[struct{}]{Status: operationresult.Success}, nil
	}
	attempt := func(ctx context.Context, observation agentshare.CallObservation) (operationresult.Result[agentshare.CallOutput], error) {
		var err error
		result, err = c.callOnce(ctx, client, req, observation, stream, toolsEnabled)
		if err != nil {
			return operationresult.Result[agentshare.CallOutput]{}, err
		}
		return operationresult.Result[agentshare.CallOutput]{Status: operationresult.Success, Data: agentshare.CallOutput{Text: result.RawText, SourceText: result.RawText, ReasoningContent: result.ReasoningContent, Usage: result.Usage, ToolCalls: result.ToolCalls}}, nil
	}
	final, err := c.Calls.Call(ctx, session, selection, messages, tools, pending, c.Messages.Committer("append_pending_user_message"), stream, prepare, attempt)
	if err != nil {
		return operationresult.Result[llmCallResult]{}, err
	}
	if final.Status != operationresult.Success {
		return operationresult.Result[llmCallResult]{Status: final.Status, Message: final.Message}, nil
	}
	result.Messages = baseMessages
	data := final.Data
	result.Text, result.Usage, result.ToolCalls, result.Outputs = data.Text, data.Usage, data.ToolCalls, data.Outputs
	result.ReasoningContent = data.ReasoningContent
	return operationresult.Result[llmCallResult]{Status: operationresult.Success, Data: result}, nil
}

func (c *Caller) callOnce(ctx context.Context, client chatcompletions.Streamer, req chatcompletions.Request, observation agentshare.CallObservation, stream delivery.MessageStream, toolsEnabled bool) (result llmCallResult, callErr error) {
	startedAt := time.Now()
	completed := observation.Event()
	defer func() {
		completed.ToolCalls = append([]llm.ToolCallRequest(nil), result.ToolCalls...)
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
		observation.LogRecord().ResultStatus = events.LogStatus(err)
		if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
			return llmCallResult{}, err
		}
		completed.ProviderError, completed.Err = true, err
		observation.LogRecord().ResultStatus = events.ResultFailed
		completed.ElapsedMS = elapsedMillis(startedAt)
		return llmCallResult{}, err
	}
	var assistant strings.Builder
	var reasoning strings.Builder
	var usage *llm.Usage
	var toolCalls []llm.ToolCallRequest
	for chunk := range ch {
		if chunk.Error != nil {
			completed.Err = chunk.Error
			completed.Text, completed.SourceText, completed.Usage, completed.ToolCallCount = assistant.String(), assistant.String(), usage, len(toolCalls)
			completed.OutputReady = assistant.Len() > 0 || len(toolCalls) > 0
			observation.LogRecord().ResultStatus = events.LogStatus(chunk.Error)
			if ctx.Err() != nil && errors.Is(chunk.Error, ctx.Err()) {
				content := assistant.String()
				return llmCallResult{Text: content, RawText: content, ReasoningContent: reasoning.String(), Usage: usage, ToolCalls: toolCalls}, chunk.Error
			}
			completed.ProviderError, completed.Err = true, chunk.Error
			observation.LogRecord().ResultStatus = events.ResultFailed
			completed.ElapsedMS = elapsedMillis(startedAt)
			return llmCallResult{Text: assistant.String(), RawText: assistant.String(), ReasoningContent: reasoning.String(), Usage: usage, ToolCalls: toolCalls}, chunk.Error
		}
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
		if chunk.DeltaReasoningContent != "" {
			reasoning.WriteString(chunk.DeltaReasoningContent)
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
				completed.Err = err
				observation.LogRecord().ResultStatus = events.LogStatus(err)
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
		observation.LogRecord().ResultStatus = events.ResultCanceled
		return llmCallResult{Text: content, RawText: content, ReasoningContent: reasoning.String(), Usage: usage, ToolCalls: toolCalls}, err
	}
	_, err = stream.Send(ctx, delivery.StreamEvent{Type: delivery.StreamFinish})
	observation.LogRecord().ResultStatus = events.ResultSucceeded
	if err != nil {
		completed.Err = err
		observation.LogRecord().ResultStatus = events.LogStatus(err)
	}
	return llmCallResult{Text: content, RawText: content, ReasoningContent: reasoning.String(), Usage: usage, ToolCalls: toolCalls}, err
}

func elapsedMillis(startedAt time.Time) int64 {
	return time.Since(startedAt).Milliseconds()
}
