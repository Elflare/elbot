package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"elbot/internal/delivery"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/modelmgr"
	notificationrules "elbot/internal/notification/rules"
	"elbot/internal/platform"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"elbot/internal/toolrun"
)

// modelCaller owns one model request. Selection is a caller-provided snapshot.
type modelCaller struct {
	messages          storage.MessageRepository
	media             *media.Manager
	hooks             *hookBridge
	identity          *identityResolver
	toolState         *toolrun.StateService
	toolRuntime       *toolRuntimeState
	completed         *signal.Signal[ModelCallCompletedEvent]
	vision            *signal.Signal[VisionFallbackUsedEvent]
	persistenceFailed *signal.Signal[PersistenceFailedEvent]
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

func (c *modelCaller) Call(ctx context.Context, session *storage.Session, selection modelmgr.Selection, messages []llm.LLMMessage, tools []llm.ToolSchema, pending *pendingUserMessage, stream delivery.MessageStream, out turnOutput) (result llmCallResult, callErr error) {
	sessionID := session.ID
	toolsEnabled := session.Mode == storage.SessionModeWork || session.Mode == storage.SessionModeBackground
	if !toolsEnabled {
		tools = nil
	}
	startedAt := time.Now()
	completed := ModelCallCompletedEvent{Provider: selection.Provider, Model: selection.Model, ElapsedMS: -1}
	published := false
	publishCompleted := func() {
		if published {
			return
		}
		published = true
		completed.EventMeta = eventMeta(ctx, sessionID)
		if completed.ElapsedMS < 0 {
			completed.ElapsedMS = elapsedMillis(startedAt)
		}
		completed.Usage = cloneUsage(completed.Usage)
		if completed.Err == nil {
			completed.Err = callErr
		}
		emitFact(ctx, c.completed, completed)
	}
	defer publishCompleted()
	var allowedTools map[string]bool
	if session.Mode == storage.SessionModeBackground {
		cached, err := cachedToolsForSession(ctx, c.toolState, c.toolRuntime.registry, session)
		if err != nil {
			return llmCallResult{}, err
		}
		allowedTools = toolrun.BackgroundToolNames(ctx, cached)
	}
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
	event, err := c.hooks.Run(ctx, hook.Event{
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
			if persistErr := persistTurnMessage(ctx, c.messages, c.media, c.persistenceFailed, &pending.message, "append_pending_user_message"); persistErr != nil {
				err = errors.Join(err, persistErr)
			}
		}
		return llmCallResult{}, fmt.Errorf("llm request hook: %w", err)
	}
	tools = event.LLM.Tools
	if allowedTools != nil {
		filtered := make([]llm.ToolSchema, 0, len(tools))
		for _, schema := range tools {
			if allowedTools[schema.Function.Name] {
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
		segments := materializeMedia(ctx, c.media, event.Message.Segments)
		baseMessages[pending.messageIndex].Segments = segments
		pending.message.Content = llm.SegmentsContentText(segments)
		pending.message.Segments = storedMessageSegments(segments)
		if err := persistTurnMessage(ctx, c.messages, c.media, c.persistenceFailed, &pending.message, "append_pending_user_message"); err != nil {
			return llmCallResult{}, err
		}
	}
	requestMessages := baseMessages
	if c.media != nil {
		seen := map[string]bool{}
		for _, message := range baseMessages {
			for _, segment := range message.Segments {
				if segment.MediaID == "" || seen[segment.MediaID] {
					continue
				}
				seen[segment.MediaID] = true
				release, err := c.media.Hold(ctx, segment.MediaID)
				if err != nil {
					return llmCallResult{}, err
				}
				defer release()
			}
		}
		var cleanup func()
		requestMessages, cleanup, err = c.media.ResolveForLLM(ctx, baseMessages)
		if err != nil {
			return llmCallResult{}, err
		}
		defer cleanup()
	}
	req := llm.ChatRequest{
		Model:     selection.Model,
		SessionID: sessionID,
		Messages:  requestMessages,
		Tools:     tools,
	}
	if selection.Client == nil {
		return llmCallResult{}, fmt.Errorf("client not found for provider %q", selection.Provider)
	}
	ch, err := selection.Client.ChatStream(ctx, req)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return llmCallResult{Messages: baseMessages, Stream: stream}, nil
		}
		if shouldFallbackVision(requestMessages, err) {
			completed.Err, completed.VisionFallback = err, true
			publishCompleted()
			emitFact((executionView{}).Context(ctx), c.vision, VisionFallbackUsedEvent{EventMeta: eventMeta(ctx, sessionID), Visible: c.identity.IsCLI(ctx) && !isBackgroundSession(session)})
			return c.Call(ctx, session, selection, fallbackVisionMessages(baseMessages), tools, nil, stream, out)
		}
		completed.ProviderError, completed.Err = true, err
		completed.ElapsedMS = elapsedMillis(startedAt)
		c.hooks.notifyError(ctx, hook.Event{Point: hook.PointLLMResponseReceived, Session: hook.SessionContext{ID: sessionID}, LLM: hook.LLMPayload{Provider: selection.Provider, Model: selection.Model, ElapsedMS: elapsedMillis(startedAt)}}, err)
		return llmCallResult{}, fmt.Errorf("chat: %w", err)
	}
	var assistant strings.Builder
	var usage *llm.Usage
	var toolCalls []llm.ToolCallRequest
	showReasoning := c.identity.IsCLI(ctx)
	reasoningOpen := false
	for chunk := range ch {
		if chunk.Error != nil {
			if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				content := assistant.String()
				return llmCallResult{Text: content, RawText: content, Usage: usage, ToolCalls: toolCalls, Messages: baseMessages, Stream: stream}, nil
			}
			if shouldFallbackVision(requestMessages, chunk.Error) {
				completed.Err, completed.VisionFallback = chunk.Error, true
				publishCompleted()
				emitFact((executionView{}).Context(ctx), c.vision, VisionFallbackUsedEvent{EventMeta: eventMeta(ctx, sessionID), Visible: c.identity.IsCLI(ctx) && !isBackgroundSession(session)})
				return c.Call(ctx, session, selection, fallbackVisionMessages(baseMessages), tools, nil, stream, out)
			}
			completed.ProviderError, completed.Err = true, chunk.Error
			completed.ElapsedMS = elapsedMillis(startedAt)
			c.hooks.notifyError(ctx, hook.Event{Point: hook.PointLLMResponseReceived, Session: hook.SessionContext{ID: sessionID}, LLM: hook.LLMPayload{Provider: selection.Provider, Model: selection.Model, SourceText: assistant.String(), Text: assistant.String(), ToolCalls: toolCalls, Usage: usage, ElapsedMS: elapsedMillis(startedAt)}}, chunk.Error)
			out.SendNotice(ctx, slog.LevelError, notificationrules.ModelInterrupted(chunk.Error))

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
		if toolsEnabled {
			for _, delta := range chunk.ToolCallDeltas {
				toolCalls = append(toolCalls, llm.ToolCallRequest{ID: delta.ID, Name: delta.Name, Arguments: delta.Args})
			}
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
	completed.ElapsedMS = elapsedMs
	content := assistant.String()
	event, err = c.hooks.Run(ctx, hook.Event{
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
	if !toolsEnabled {
		toolCalls = nil
	}
	finalText := event.LLM.Text
	completed.OutputReady = true
	completed.Text, completed.SourceText, completed.ToolCallCount, completed.Usage = finalText, event.LLM.SourceText, len(toolCalls), usage
	return llmCallResult{Text: finalText, RawText: content, Usage: usage, ToolCalls: toolCalls, Outputs: event.Outputs, Messages: baseMessages, Stream: stream}, nil
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

func platformSegmentsToLLM(segments []platform.MessageSegment, fallbackText string) []llm.MessageSegment {
	out := make([]llm.MessageSegment, 0, len(segments))
	for _, segment := range segments {
		switch segment.Type {
		case platform.SegmentText:
			if segment.Text != "" {
				out = append(out, llm.MessageSegment{Type: llm.SegmentText, Text: segment.Text})
			}
		case platform.SegmentImage:
			if segment.URL != "" || segment.MediaID != "" {
				out = append(out, llm.MessageSegment{Type: llm.SegmentImage, MediaID: segment.MediaID, URL: segment.URL, MIMEType: segment.MIMEType, Name: segment.Name})
			} else {
				out = append(out, llm.MessageSegment{Type: llm.SegmentText, Text: fileSegmentText(segment.Name, "图片")})
			}
		case platform.SegmentFile:
			// TODO: 后续支持语音、视频和普通文件的真实模型输入；当前统一回滚为文本描述。
			out = append(out, llm.MessageSegment{Type: llm.SegmentFile, MediaID: segment.MediaID, URL: segment.URL, Text: fileSegmentText(segment.Name, segment.Text), MIMEType: segment.MIMEType, Name: segment.Name})
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

func elapsedMillis(startedAt time.Time) int64 {
	return time.Since(startedAt).Milliseconds()
}
