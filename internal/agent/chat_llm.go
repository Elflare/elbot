package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"elbot/internal/config"
	"elbot/internal/delivery"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/platform"
)

type llmCallResult struct {
	Text      string
	RawText   string
	Usage     *llm.Usage
	ToolCalls []llm.ToolCallRequest
	Outputs   []delivery.Output
	Messages  []llm.LLMMessage
	Stream    delivery.MessageStream
}

func (a *Agent) callLLM(ctx context.Context, sessionID string, selection config.ModelSelection, messages []llm.LLMMessage, tools []llm.ToolSchema, pending *pendingUserMessage, stream delivery.MessageStream, out turnOutput) (llmCallResult, error) {
	startedAt := time.Now()
	baseMessages := llm.CloneMessages(messages)
	hookMessage := hook.MessagePayload{}
	if pending != nil {
		hookMessage = hook.MessagePayload{
			ID:           pending.message.ID,
			Role:         string(llm.RoleUser),
			PlatformText: pending.platformText,
			Segments:     append([]llm.MessageSegment(nil), baseMessages[pending.messageIndex].Segments...),
		}
	}
	event, err := a.runHook(ctx, hook.Event{
		Point:   hook.PointLLMRequestPrepared,
		Session: hook.SessionContext{ID: sessionID},
		Message: hookMessage,
		LLM: hook.LLMPayload{
			Provider: selection.Provider,
			Model:    selection.Model,
			Messages: llm.CloneMessages(baseMessages),
			Tools:    tools,
		},
	})
	if err != nil {
		if pending != nil {
			if persistErr := a.persistTurnMessage(ctx, &pending.message, "append_pending_user_message"); persistErr != nil {
				err = errors.Join(err, persistErr)
			}
		}
		return llmCallResult{}, fmt.Errorf("llm request hook: %w", err)
	}
	selection.Provider = event.LLM.Provider
	selection.Model = event.LLM.Model
	tools = event.LLM.Tools
	if pending != nil {
		segments := append([]llm.MessageSegment(nil), event.Message.Segments...)
		baseMessages[pending.messageIndex].Segments = segments
		pending.message.Content = llm.SegmentsContentText(segments)
		pending.message.Segments = storedMessageSegments(segments)
		if err := a.persistTurnMessage(ctx, &pending.message, "append_pending_user_message"); err != nil {
			return llmCallResult{}, err
		}
	}
	requestMessages := baseMessages
	req := llm.ChatRequest{
		Model:     selection.Model,
		SessionID: sessionID,
		Messages:  requestMessages,
		Tools:     tools,
	}
	ch, err := a.clientForProvider(selection.Provider).ChatStream(ctx, req)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return llmCallResult{Messages: baseMessages, Stream: stream}, nil
		}
		if shouldFallbackVision(requestMessages, err) {
			a.notifyVisionFallbackOnce(ctx, sessionID, out)
			return a.callLLM(ctx, sessionID, selection, fallbackVisionMessages(baseMessages), tools, nil, stream, out)
		}
		a.audit("llm_error", "session_id", sessionID, "provider", selection.Provider, "model", selection.Model, "elapsed_ms", elapsedMillis(startedAt), "error", err.Error())
		a.notifyHookError(ctx, hook.Event{Point: hook.PointLLMResponseReceived, Session: hook.SessionContext{ID: sessionID}, LLM: hook.LLMPayload{Provider: selection.Provider, Model: selection.Model, ElapsedMS: elapsedMillis(startedAt)}}, err)
		return llmCallResult{}, fmt.Errorf("chat: %w", err)
	}
	var assistant strings.Builder
	var usage *llm.Usage
	var toolCalls []llm.ToolCallRequest
	showReasoning := a.shouldShowCLIReasoning(ctx)
	reasoningOpen := false
	for chunk := range ch {
		if chunk.Error != nil {
			if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				content := assistant.String()
				return llmCallResult{Text: content, RawText: content, Usage: usage, ToolCalls: toolCalls, Messages: baseMessages, Stream: stream}, nil
			}
			if shouldFallbackVision(requestMessages, chunk.Error) {
				a.notifyVisionFallbackOnce(ctx, sessionID, out)
				return a.callLLM(ctx, sessionID, selection, fallbackVisionMessages(baseMessages), tools, nil, stream, out)
			}
			a.audit("llm_error", "session_id", sessionID, "provider", selection.Provider, "model", selection.Model, "elapsed_ms", elapsedMillis(startedAt), "error", chunk.Error.Error())
			a.notifyHookError(ctx, hook.Event{Point: hook.PointLLMResponseReceived, Session: hook.SessionContext{ID: sessionID}, LLM: hook.LLMPayload{Provider: selection.Provider, Model: selection.Model, SourceText: assistant.String(), Text: assistant.String(), ToolCalls: toolCalls, Usage: usage, ElapsedMS: elapsedMillis(startedAt)}}, chunk.Error)
			out.SendNotice(ctx, fmt.Sprintf("LLM 响应中断：%v", chunk.Error))

			return llmCallResult{}, markUserNotified(fmt.Errorf("chat stream: %w", chunk.Error))
		}
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
		if chunk.DeltaReasoningContent != "" && showReasoning {
			if !reasoningOpen {
				out.SendReasoning(ctx, "[thinking] ")
				reasoningOpen = true
			}
			out.SendReasoning(ctx, chunk.DeltaReasoningContent)
		}
		for _, delta := range chunk.ToolCallDeltas {
			toolCalls = append(toolCalls, llm.ToolCallRequest{ID: delta.ID, Name: delta.Name, Arguments: delta.Args})
		}
		delta := chunk.DeltaContent
		assistant.WriteString(delta)
		if stream != nil && delta != "" {
			if err := stream.Append(ctx, delta); err != nil {
				return llmCallResult{}, fmt.Errorf("stream append: %w", err)
			}
		}
	}
	if reasoningOpen {
		out.SendReasoning(ctx, "[/thinking]\n\n")
	}
	elapsedMs := elapsedMillis(startedAt)
	content := assistant.String()
	event, err = a.runHook(ctx, hook.Event{
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
		return llmCallResult{}, fmt.Errorf("llm response hook: %w", err)
	}
	usage = event.LLM.Usage
	toolCalls = event.LLM.ToolCalls
	finalText := event.LLM.Text
	a.logLLMOutput(sessionID, selection, finalText, event.LLM.SourceText, len(toolCalls), elapsedMs)

	a.auditUsage(sessionID, selection, usage, elapsedMs)
	return llmCallResult{Text: finalText, RawText: content, Usage: usage, ToolCalls: toolCalls, Outputs: event.Outputs, Messages: baseMessages, Stream: stream}, nil
}

func (a *Agent) logLLMOutput(sessionID string, selection config.ModelSelection, text, rawText string, toolCallCount int, elapsedMs int64) {
	if a.logger == nil {
		return
	}
	a.logger.Info("llm output",
		"event", "assistant_message",
		"session_id", sessionID,
		"provider", selection.Provider,
		"model", selection.Model,
		"elapsed_ms", elapsedMs,
		"text", previewLogText(text),
		"raw_text", previewLogText(rawText),
		"tool_call_count", toolCallCount,
	)
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

func (a *Agent) notifyVisionFallbackOnce(ctx context.Context, sessionID string, out turnOutput) {
	if !a.shouldShowCLIReasoning(ctx) {
		return
	}
	a.visionFallbackMu.Lock()
	if a.visionFallbackNotified[sessionID] {
		a.visionFallbackMu.Unlock()
		return
	}
	a.visionFallbackNotified[sessionID] = true
	a.visionFallbackMu.Unlock()
	_, _ = out.SendAssistant(ctx, "当前模型似乎不支持视觉，图片已按文本描述处理。")
}

func (a *Agent) userMessageSegments(ctx context.Context, text string) []llm.MessageSegment {
	if msg, ok := platform.MessageContextFrom(ctx); ok && len(msg.Segments) > 0 {
		return platformSegmentsToLLM(msg.Segments, text)
	}
	return llm.TextSegments(text)
}

func platformSegmentsToLLM(segments []platform.MessageSegment, fallbackText string) []llm.MessageSegment {
	out := make([]llm.MessageSegment, 0, len(segments))
	for _, segment := range segments {
		switch segment.Type {
		case platform.SegmentText:
			if segment.Text != "" {
				out = append(out, llm.MessageSegment{Type: llm.SegmentText, Text: segment.Text})
			}
		case platform.SegmentImage:
			if segment.URL != "" {
				out = append(out, llm.MessageSegment{Type: llm.SegmentImage, URL: segment.URL, MIMEType: segment.MIMEType, Name: segment.Name})
			} else {
				out = append(out, llm.MessageSegment{Type: llm.SegmentText, Text: fileSegmentText(segment.Name, "图片")})
			}
		case platform.SegmentFile:
			// TODO: 后续支持语音、视频和普通文件的真实模型输入；当前统一回滚为文本描述。
			out = append(out, llm.MessageSegment{Type: llm.SegmentFile, Text: fileSegmentText(segment.Name, segment.Text), MIMEType: segment.MIMEType, Name: segment.Name})
		}
	}
	if len(out) == 0 {
		return llm.TextSegments(fallbackText)
	}
	return out
}

func fileSegmentText(name, fallback string) string {
	fallback = strings.TrimSpace(fallback)
	if fallback == "" {
		fallback = "文件"
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "[" + fallback + "]"
	}
	return fmt.Sprintf("[%s: %s]", fallback, name)
}

func (a *Agent) isCLIContext(ctx context.Context) bool {
	if msg, ok := platform.MessageContextFrom(ctx); ok {
		return msg.Platform == "cli"
	}
	return a.platform != nil && a.platform.Name() == "cli"
}

func (a *Agent) shouldShowCLIReasoning(ctx context.Context) bool {
	return a.isCLIContext(ctx)
}

type cliReasoningSender interface {
	SendReasoning(context.Context, string) error
}

func (a *Agent) sendCLIReasoning(ctx context.Context, text string) {
	if !a.shouldShowCLIReasoning(ctx) || text == "" {
		return
	}
	if msg, ok := platform.MessageContextFrom(ctx); ok {
		if sender, ok := msg.Sender.(cliReasoningSender); ok {
			_ = sender.SendReasoning(ctx, text)
			return
		}
	}
	if sender, ok := a.platform.(cliReasoningSender); ok {
		_ = sender.SendReasoning(ctx, text)
	}
}

func (a *Agent) auditUsage(sessionID string, selection config.ModelSelection, usage *llm.Usage, elapsedMs int64) {
	attrs := []any{"session_id", sessionID, "provider", selection.Provider, "model", selection.Model, "elapsed_ms", elapsedMs}
	if usage != nil {
		attrs = append(attrs,
			"prompt_tokens", usage.PromptTokens,
			"completion_tokens", usage.CompletionTokens,
			"total_tokens", usage.TotalTokens,
			"cache_hit_tokens", usage.CacheHitTokens,
		)
	}
	a.audit("llm_usage", attrs...)
}

func elapsedMillis(startedAt time.Time) int64 {
	return time.Since(startedAt).Milliseconds()
}
