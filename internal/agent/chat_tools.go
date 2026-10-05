package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"elbot/internal/llm"
	sessionpkg "elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/toolrun"
	"elbot/internal/turn"
)

type pendingUserMessage struct {
	message      storage.Message
	messageIndex int
	platformText string
}

func (r *chatRunner) drainPendingUserInput(sessionID string, messages []llm.LLMMessage, expected ...string) ([]llm.LLMMessage, *pendingUserMessage) {
	pending := r.turns.DrainMergedInput(sessionID, expected...)
	if pending.Text == "" && len(pending.Segments) == 0 {
		return messages, nil
	}
	segments := append([]llm.MessageSegment(nil), pending.Segments...)
	if len(segments) == 0 {
		segments = llm.TextSegments(pending.Text)
	}
	message := storage.Message{
		ID:        storage.NewID(),
		SessionID: sessionID,
		Role:      storage.RoleUser,
		Content:   llm.SegmentsContentText(segments),
		Segments:  storedMessageSegments(segments),
	}
	binding := &pendingUserMessage{message: message, messageIndex: len(messages), platformText: pending.PlatformText}
	return append(messages, llm.LLMMessage{Role: llm.RoleUser, Segments: segments}), binding
}

func (r *chatRunner) executeToolCalls(ctx context.Context, session *storage.Session, calls []llm.ToolCallRequest, assistantText, assistantRawText string, out turnOutput) toolrun.RunResult {
	if session == nil || (session.Mode != storage.SessionModeWork && session.Mode != storage.SessionModeBackground) {
		return toolrun.RunResult{}
	}
	cached, err := cachedToolsForSession(ctx, r.toolState, r.toolRuntime.registry, session)
	if err != nil {
		messages := make([]llm.LLMMessage, 0, len(calls))
		transcript := []storage.Message{toolCallStorageMessage(session.ID, assistantText, assistantRawText, calls)}
		for _, call := range calls {
			message := llm.LLMMessage{Role: llm.RoleTool, Name: call.Name, ToolCallID: call.ID, Segments: llm.TextSegments(fmt.Sprintf("tool call %s failed: load tool state: %v", call.Name, err))}
			messages = append(messages, message)
			transcript = append(transcript, toolResultStorageMessage(session.ID, message))
		}
		return toolrun.RunResult{Messages: messages, PreparedCalls: calls, Transcript: transcript}
	}
	return r.toolRuntime.manager.Run(ctx, r.toolDeps.forTurn(out, turn.AttemptFromContext(ctx)), toolrun.RunRequest{
		Session:          session,
		Calls:            calls,
		AssistantText:    assistantText,
		AssistantRawText: assistantRawText,
		CachedTools:      cached,
		Actor:            r.identity.Actor(ctx),
	})
}

func riskReasonsText(reasons []string) string {
	if len(reasons) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n风险原因：")
	for _, reason := range reasons {
		reason = strings.TrimSpace(reason)
		if reason != "" {
			sb.WriteString("\n- ")
			sb.WriteString(reason)
		}
	}
	return sb.String()
}

func (r *chatRunner) maxToolRoundsPerTurn() int {
	if r.toolRuntime.config.MaxRoundsPerTurn <= 0 {
		return 2
	}
	return r.toolRuntime.config.MaxRoundsPerTurn
}

func skippedToolMessages(calls []llm.ToolCallRequest, maxRounds int) []llm.LLMMessage {
	messages := make([]llm.LLMMessage, 0, len(calls))
	for _, call := range calls {
		messages = append(messages, llm.LLMMessage{
			Role:       llm.RoleTool,
			Name:       call.Name,
			ToolCallID: call.ID,
			Segments:   llm.TextSegments(fmt.Sprintf("tool call skipped: max_rounds_per_turn=%d reached. Please summarize current progress without calling more tools.", maxRounds)),
		})
	}
	return messages
}

func previewLogText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	const maxPreviewRunes = 120
	if len([]rune(text)) <= maxPreviewRunes {
		return text
	}
	runes := []rune(text)
	return string(runes[:maxPreviewRunes]) + "..."
}

func joinAssistantText(first, second string) string {
	first = strings.TrimSpace(first)
	second = strings.TrimSpace(second)
	switch {
	case first == "":
		return second
	case second == "":
		return first
	default:
		return first + "\n\n" + second
	}
}

func joinToolNames(calls []llm.ToolCallRequest) string {
	names := make([]string, 0, len(calls))
	for _, call := range calls {
		if call.Name != "" {
			names = append(names, call.Name)
		}
	}
	return strings.Join(names, ", ")
}

func compactArguments(args string) string {
	args = strings.TrimSpace(args)
	if args == "" {
		return "{}"
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(args)); err == nil {
		return compact.String()
	}
	return args
}

func previewArguments(args string) string {
	args = compactArguments(args)
	const maxPreviewRunes = 160
	if len([]rune(args)) <= maxPreviewRunes {
		return args
	}
	runes := []rune(args)
	return string(runes[:maxPreviewRunes]) + "..."
}

func (r *chatRunner) toolsForSession(ctx context.Context, session *storage.Session) ([]llm.ToolSchema, error) {
	if session == nil || (session.Mode != storage.SessionModeWork && session.Mode != storage.SessionModeBackground) {
		return nil, nil
	}
	if session.Mode == storage.SessionModeWork && r.toolRuntime.provider != nil && !r.toolRuntime.defaultProvider {
		return r.toolRuntime.provider.Schemas(ctx, session.Mode, session, r.identity.Scope(ctx))
	}
	cached, err := cachedToolsForSession(ctx, r.toolState, r.toolRuntime.registry, session)
	if err != nil {
		return nil, err
	}
	return r.toolRuntime.manager.Schemas(ctx, toolrun.Context{Mode: session.Mode, Session: session, Scope: r.identity.Scope(ctx), Actor: r.identity.Actor(ctx), DisableBaseTools: isBackgroundSession(session)}, cached)
}

func isBackgroundSession(row *storage.Session) bool { return sessionpkg.IsBackground(row) }
