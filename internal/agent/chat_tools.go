package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"elbot/internal/llm"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/toolrun"
)

func (a *Agent) drainPendingUserInput(sessionID string, messages []llm.LLMMessage, transcript *[]storage.Message) []llm.LLMMessage {
	pending := a.turns.DrainMerged(sessionID)
	if pending == "" {
		return messages
	}
	// 工具执行期间的用户追问不打断工具；在下一次 LLM 调用前作为补充输入注入，并持久化进本轮 transcript。
	return appendPendingUserInput(messages, transcript, pending)
}

func appendPendingUserInput(messages []llm.LLMMessage, transcript *[]storage.Message, content string) []llm.LLMMessage {
	if content == "" {
		return messages
	}
	if transcript != nil {
		*transcript = append(*transcript, storage.Message{Role: storage.RoleUser, Content: content})
	}
	return append(messages, llm.LLMMessage{Role: llm.RoleUser, Segments: llm.TextSegments(content)})
}

func (a *Agent) executeToolCalls(ctx context.Context, session *storage.Session, calls []llm.ToolCallRequest, assistantText, assistantRawText string, out turnOutput) ([]llm.LLMMessage, string, []storage.Message, bool) {
	result := a.toolRunManager().Run(ctx, agentToolRunDeps{agent: a, output: out}, toolrun.RunRequest{
		Session:          session,
		Calls:            calls,
		AssistantText:    assistantText,
		AssistantRawText: assistantRawText,
		CachedTools:      a.cachedToolsForSession(session),
		Actor:            a.actor(ctx),
	})
	return result.Messages, result.ConfirmationExtra, result.Transcript, result.Stopped
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

func (a *Agent) isSessionAutoConfirmed(sessionID string) bool {
	a.autoConfirmMu.Lock()
	defer a.autoConfirmMu.Unlock()
	return a.autoConfirmSession[sessionID]
}

func (a *Agent) setSessionAutoConfirmed(sessionID string) {
	a.autoConfirmMu.Lock()
	defer a.autoConfirmMu.Unlock()
	a.autoConfirmSession[sessionID] = true
}

func (a *Agent) isToolAutoConfirmed(sessionID, toolName string) bool {
	a.autoConfirmMu.Lock()
	defer a.autoConfirmMu.Unlock()
	return a.autoConfirmTools[sessionID] != nil && a.autoConfirmTools[sessionID][toolName]
}

func (a *Agent) setToolAutoConfirmed(sessionID, toolName string) {
	a.autoConfirmMu.Lock()
	defer a.autoConfirmMu.Unlock()
	if a.autoConfirmTools[sessionID] == nil {
		a.autoConfirmTools[sessionID] = map[string]bool{}
	}
	a.autoConfirmTools[sessionID][toolName] = true
}

func (a *Agent) maxToolRoundsPerTurn() int {
	if a.toolRuntime.config.MaxRoundsPerTurn <= 0 {
		return 2
	}
	return a.toolRuntime.config.MaxRoundsPerTurn
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

func (a *Agent) toolCallRisk(ctx context.Context, call llm.ToolCallRequest) string {
	if a.toolRuntime.registry == nil {
		return "unknown"
	}
	t, ok := a.toolRuntime.registry.Get(call.Name)
	if !ok {
		return "unknown"
	}
	assessment, err := tool.AssessRisk(ctx, t, tool.CallRequest{ID: call.ID, Name: call.Name, Arguments: json.RawMessage(call.Arguments)})
	if err != nil {
		return "unknown"
	}
	return string(assessment.Level)
}

func (a *Agent) recordToolCall(ctx context.Context, sessionID string, call llm.ToolCallRequest, risk string, startedAt time.Time, result string, callErr error) {
	record := &storage.ToolCallRecord{
		SessionID:     sessionID,
		ToolCallID:    call.ID,
		ToolName:      call.Name,
		ActorID:       a.actor(ctx).ID,
		RiskLevel:     risk,
		Success:       callErr == nil,
		ResultPreview: previewLogText(result),
		StartedAt:     startedAt,
		FinishedAt:    storage.Now(),
	}
	if callErr != nil {
		record.Error = callErr.Error()
	}
	if a.store != nil && a.store.ToolCalls() != nil {
		if err := a.store.ToolCalls().Create(ctx, record); err != nil && a.logger != nil {
			a.logger.Warn("record tool call failed", "session_id", sessionID, "tool", call.Name, "error", err)
		}
	}
	if a.logger != nil {
		a.logger.Info("tool call",
			"event", "tool_call",
			"session_id", sessionID,
			"arguments", previewArguments(call.Arguments),
			"result", previewLogText(result),
			"tool", call.Name,
			"tool_call_id", call.ID,
			"actor_id", record.ActorID,
			"risk", risk,
			"success", record.Success,
			"elapsed_ms", record.FinishedAt.Sub(record.StartedAt).Milliseconds(),
			"error", record.Error,
		)
	}
	a.audit("tool_call",
		"session_id", sessionID,
		"arguments", previewArguments(call.Arguments),
		"tool", call.Name,
		"tool_call_id", call.ID,
		"actor_id", record.ActorID,
		"risk", risk,
		"success", record.Success,
		"elapsed_ms", record.FinishedAt.Sub(record.StartedAt).Milliseconds(),
		"error", record.Error,
	)
}

func (a *Agent) logRiskConfirmationWait(sessionID string, call llm.ToolCallRequest, risk tool.RiskLevel, reasons []string) {
	auditAttrs := []any{"session_id", sessionID, "tool", call.Name, "risk", risk, "arguments", previewArguments(call.Arguments)}
	if len(reasons) > 0 {
		auditAttrs = append(auditAttrs, "risk_reasons", strings.Join(reasons, "; "))
	}
	a.audit("risk_confirmation_wait", auditAttrs...)
}

func (a *Agent) logRiskConfirmationResult(sessionID string, call llm.ToolCallRequest, risk tool.RiskLevel, action, extra, reason string) {
	a.audit("risk_confirmation_result", "session_id", sessionID, "tool", call.Name, "risk", risk, "action", action, "extra", extra, "reason", reason)
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

func (a *Agent) toolsForSession(ctx context.Context, session *storage.Session) ([]llm.ToolSchema, error) {
	if session == nil || session.Mode != storage.SessionModeWork {
		return nil, nil
	}
	if a.toolRuntime.provider != nil && !a.toolRuntime.defaultProvider {
		return a.toolRuntime.provider.Schemas(ctx, session.Mode, session, a.scope(ctx))
	}
	return a.toolRunManager().Schemas(ctx, toolrun.Context{Mode: session.Mode, Session: session, Scope: a.scope(ctx), Actor: a.actor(ctx), DisableBaseTools: isBackgroundSession(session)}, a.cachedToolsForSession(session))
}

func isBackgroundSession(session *storage.Session) bool {
	if session == nil {
		return false
	}
	return strings.TrimSpace(decodeSessionMetadata(session.Metadata).BackgroundKind) != ""
}
