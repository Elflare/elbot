package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/output"
	"elbot/internal/request"
	"elbot/internal/security"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/turn"
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

func appendAssistantTextMessage(messages []llm.LLMMessage, content, rawContent string) []llm.LLMMessage {
	if rawContent == "" {
		rawContent = content
	}
	if rawContent == "" {
		return messages
	}
	return append(messages, llm.LLMMessage{Role: llm.RoleAssistant, Segments: llm.TextSegments(rawContent)})
}

func (a *Agent) executeToolCalls(ctx context.Context, session *storage.Session, calls []llm.ToolCallRequest, assistantText, assistantRawText string) ([]llm.LLMMessage, string, []storage.Message, bool) {
	sessionID := session.ID

	executor := tool.Executor{Registry: a.toolRegistry, Actor: a.actor(ctx), Policy: a.securityPolicy}
	messages := make([]llm.LLMMessage, 0, len(calls))
	preparedCalls := make([]llm.ToolCallRequest, 0, len(calls))
	transcript := []storage.Message{}
	var confirmationExtra string
	for _, call := range calls {
		startedAt := storage.Now()
		toolEvent, err := a.runHook(ctx, hook.Event{
			Point:   hook.PointToolCallPrepared,
			Session: a.hookSession(session),
			Tool:    hook.ToolPayload{ID: call.ID, Name: call.Name, Arguments: call.Arguments},
		})
		if err != nil {
			preparedCalls = append(preparedCalls, call)
			message := toolMessage(call.Name, call.ID, fmt.Sprintf("tool call %s failed: hook: %v", call.Name, err))
			messages = append(messages, message)
			transcript = append(transcript, toolResultStorageMessage(sessionID, message))
			continue
		}
		call.ID = toolEvent.Tool.ID
		call.Name = toolEvent.Tool.Name
		call.Arguments = toolEvent.Tool.Arguments
		preparedCalls = append(preparedCalls, call)
		risk := a.toolCallRisk(ctx, call)
		a.turns.AddToolUse(sessionID, call.Name)
		if a.isCLIContext(ctx) || strings.TrimSpace(assistantText) == "" {
			a.sendPreview(ctx, fmt.Sprintf("正在调用 %s：%s", call.Name, previewArguments(call.Arguments)))
		}
		_, toolCtx, done, err := a.requests.Start(ctx, request.StartRequest{SessionID: sessionID, Kind: request.KindTool, Label: call.Name})
		if err != nil {
			content := fmt.Sprintf("tool call %s failed: %v", call.Name, err)
			message := toolMessage(call.Name, call.ID, content)
			a.recordToolCall(ctx, sessionID, call, risk, startedAt, content, err)
			messages = append(messages, message)
			transcript = append(transcript, toolResultStorageMessage(sessionID, message))
			continue
		}
		if allowed, extra, message, stopped := a.confirmToolCallIfNeeded(ctx, sessionID, call); !allowed {
			done()
			if stopped {
				return messages, confirmationExtra, transcript, true
			}
			messageText := llm.SegmentsContentText(message.Segments)
			a.recordToolCall(ctx, sessionID, call, risk, startedAt, messageText, fmt.Errorf("%s", messageText))
			messages = append(messages, message)
			transcript = append(transcript, toolResultStorageMessage(sessionID, message))
			if extra != "" {
				confirmationExtra = joinAssistantText(confirmationExtra, extra)
			}
			continue
		} else if extra != "" {
			confirmationExtra = joinAssistantText(confirmationExtra, extra)
		}
		result := executor.Execute(toolCtx, call)
		done()
		completedEvent, hookErr := a.runHook(ctx, hook.Event{
			Point:   hook.PointToolCallCompleted,
			Session: a.hookSession(session),
			Tool: hook.ToolPayload{
				ID:        call.ID,
				Name:      call.Name,
				Arguments: call.Arguments,
				Risk:      string(risk),
				Result:    llm.SegmentsContentText(result.Message.Segments),
				Error:     result.Err,
			},
		})
		if hookErr != nil {
			result.Err = hookErr
			result.Message.Segments = llm.TextSegments(fmt.Sprintf("tool call %s failed: hook: %v", call.Name, hookErr))
		} else {
			result.Message.Segments = llm.TextSegments(completedEvent.Tool.Result)
		}
		resultText := llm.SegmentsContentText(result.Message.Segments)
		a.recordToolCall(ctx, sessionID, call, risk, startedAt, resultText, result.Err)
		messages = append(messages, result.Message)
		if call.Name == "discover_tool" && result.Result != nil {
			a.rememberDiscoveryResult(ctx, session, result.Result)
		}
		if result.Result != nil && len(result.Result.Outputs) > 0 {
			if err := a.sendOutputs(ctx, result.Result.Outputs); err != nil {
				result.Err = err
				result.Message.Segments = llm.TextSegments(fmt.Sprintf("tool call %s failed: output: %v", call.Name, err))
			}
		}
		transcript = append(transcript, toolResultStorageMessage(sessionID, persistedToolMessage(result.Message)))
		if result.Err != nil {
			a.sendPreview(ctx, fmt.Sprintf("%s 调用失败：%v", call.Name, result.Err))
		}
	}
	transcript = append([]storage.Message{toolCallStorageMessage(sessionID, assistantText, assistantRawText, preparedCalls)}, transcript...)
	return messages, confirmationExtra, transcript, false
}

func toolMessage(name, id, content string) llm.LLMMessage {
	return llm.LLMMessage{Role: llm.RoleTool, Name: name, ToolCallID: id, Segments: llm.TextSegments(content)}
}

func (a *Agent) confirmToolCallIfNeeded(ctx context.Context, sessionID string, call llm.ToolCallRequest) (bool, string, llm.LLMMessage, bool) {
	message := llm.LLMMessage{Role: llm.RoleTool, Name: call.Name, ToolCallID: call.ID}
	if a.toolRegistry == nil {
		message.Segments = llm.TextSegments(fmt.Sprintf("tool call %s failed: tool registry is not configured", call.Name))
		return false, "", message, false
	}
	t, ok := a.toolRegistry.Get(call.Name)
	if !ok {
		message.Segments = llm.TextSegments(fmt.Sprintf("tool call %s failed: tool not found", call.Name))
		return false, "", message, false
	}
	assessment, err := tool.AssessRisk(ctx, t, tool.CallRequest{ID: call.ID, Name: call.Name, Arguments: json.RawMessage(call.Arguments)})
	if err != nil {
		message.Segments = llm.TextSegments(fmt.Sprintf("tool call %s failed: assess risk: %v", call.Name, err))
		return false, "", message, false
	}
	risk := assessment.Level
	actor := a.actor(ctx)
	policy := a.securityPolicy
	if policy == nil {
		policy = security.DefaultPolicy()
	}
	info := t.Info()
	if info.SuperadminOnly && actor.Role != security.RoleSuperadmin {
		a.audit("permission_denied", "actor_id", actor.ID, "session_id", sessionID, "tool", call.Name, "reason", "tool_requires_superadmin")
		message.Segments = llm.TextSegments(fmt.Sprintf("tool call %s denied: requires superadmin role", call.Name))
		return false, "", message, false
	}
	if !policy.CanUseTool(actor, risk) {
		a.audit("permission_denied", "actor_id", actor.ID, "session_id", sessionID, "tool", call.Name, "risk", risk, "reason", "tool_risk_above_allowed_level")
		message.Segments = llm.TextSegments(fmt.Sprintf("tool call %s denied: risk %s is above your allowed tool level", call.Name, risk))
		return false, "", message, false
	}
	if allowed, handled := a.confirmCronSandboxShell(ctx, sessionID, call, risk, &message); handled {
		return allowed, "", message, false
	}
	if !policy.NeedsToolConfirmation(actor, risk) || a.isSessionAutoConfirmed(sessionID) || a.isToolAutoConfirmed(sessionID, call.Name) {
		return true, "", message, false
	}

	fullArgs := compactArguments(call.Arguments)
	previewArgs := previewArguments(fullArgs)
	a.logRiskConfirmationWait(sessionID, call, risk, assessment.Reasons)
	a.sendChat(ctx, fmt.Sprintf("高风险工具调用等待确认\n工具：%s\n风险：%s\n参数：%s%s\n%s。\n", call.Name, risk, previewArgs, riskReasonsText(assessment.Reasons), riskConfirmationPromptText()))
	resp, ok := a.turns.AwaitRiskConfirmation(sessionID, turn.RiskConfirmation{ID: call.ID, ToolName: call.Name, Arguments: fullArgs, Risk: string(risk), Summary: fmt.Sprintf("%s %s", call.Name, previewArgs)})
	if !ok || resp.Stopped {
		a.logRiskConfirmationResult(sessionID, call, risk, "stop", resp.Extra, "")
		return false, resp.Extra, message, true
	}
	if resp.ConfirmTool {
		a.setToolAutoConfirmed(sessionID, call.Name)
		a.sendChat(ctx, fmt.Sprintf("已为当前 Session 自动确认后续 %s 工具调用。\n", call.Name))
	}
	if resp.ConfirmAll {
		a.setSessionAutoConfirmed(sessionID)
		a.sendChat(ctx, "已为当前 Session 自动确认后续高风险工具调用。\n")
	}
	if resp.Rejected {
		reason := strings.TrimSpace(resp.Reason)
		if reason == "" {
			reason = "user rejected"
		}
		a.logRiskConfirmationResult(sessionID, call, risk, "reject", resp.Extra, reason)
		message.Segments = llm.TextSegments(fmt.Sprintf("tool call %s rejected by user: %s", call.Name, reason))
		return false, resp.Extra, message, false
	}
	action := "confirm"
	if resp.ConfirmTool {
		action = "confirmtool"
	}
	if resp.ConfirmAll {
		action = "confirmall"
	}
	a.logRiskConfirmationResult(sessionID, call, risk, action, resp.Extra, "")
	return true, resp.Extra, message, false
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
	if a.toolConfig.MaxRoundsPerTurn <= 0 {
		return 2
	}
	return a.toolConfig.MaxRoundsPerTurn
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
	if a.toolRegistry == nil {
		return "unknown"
	}
	t, ok := a.toolRegistry.Get(call.Name)
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

func (a *Agent) sendPreview(ctx context.Context, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	event, err := a.runHook(ctx, hook.Event{Point: hook.PointAgentOutputPrepared, Message: hook.MessagePayload{Role: string(llm.RoleAssistant), Segments: llm.TextSegments(text)}})
	if err != nil {
		return
	}
	body := strings.TrimSpace(llm.SegmentsTextOnly(event.Message.Segments))
	if body == "" {
		return
	}
	preview := "[tool] " + body
	_ = a.sendNoticeOutput(ctx, output.Target{}, output.Text(preview))
	a.notifyHook(ctx, hook.Event{Point: hook.PointPlatformMessageSent, Message: hook.MessagePayload{Role: string(llm.RoleAssistant), Segments: llm.TextSegments(preview)}})
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
	if session == nil || session.Mode != storage.SessionModeWork || a.tools == nil {
		return nil, nil
	}
	base, err := a.tools.Schemas(ctx, session.Mode, session, a.scope(ctx))
	if err != nil {
		return nil, err
	}
	out := make([]llm.ToolSchema, 0, len(base)+len(a.discoveredToolSchemas(session)))
	seen := map[string]bool{}
	for _, schema := range base {
		name := schema.Function.Name
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, schema)
	}
	for _, schema := range a.discoveredToolSchemas(session) {
		name := schema.Function.Name
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, schema)
	}
	return out, nil
}
