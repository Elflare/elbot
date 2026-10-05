package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"elbot/internal/config"
	"elbot/internal/contextmgr"
	"elbot/internal/delivery"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	notificationrules "elbot/internal/notification/rules"
	"elbot/internal/request"
	runtimestatus "elbot/internal/runtime"
	sessionpkg "elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

func (a *Agent) handleChat(ctx context.Context, text string) error {
	ctx, row, err := a.resolveInput(ctx, text)
	if err != nil {
		return err
	}
	return a.startChat(ctx, row, text)
}

func (a *Agent) startChat(ctx context.Context, session *storage.Session, text string) error {
	return a.startChatWithOutput(ctx, session, text, foregroundTurnOutput{agent: a})
}

func (a *Agent) startBackgroundChat(ctx context.Context, session *storage.Session, text string) error {
	return a.startChatWithOutput(ctx, session, text, backgroundTurnOutput{agent: a})
}

func (a *Agent) startChatWithOutput(ctx context.Context, row *storage.Session, text string, out turnOutput) error {
	execution := turn.ExecutionFromContext(ctx)
	if execution == nil {
		execution = turn.NewExecution(storage.NewID())
		ctx = turn.WithExecution(ctx, execution)
	}
	out = executionTurnOutput{agent: a, execution: execution, fallback: out}
	for {
		next, pending, err := a.runChatTurnWithOutput(ctx, row, text, out)
		if err != nil {
			execution.Finish(err)
			return err
		}
		if pending.Text == "" && len(pending.Segments) == 0 {
			if a.turns.Execution(next.ID) != execution {
				execution.Finish(nil)
			}
			return nil
		}
		ctx = a.executionContext(ctx)
		if next.ID != row.ID && !isBackgroundSession(next) {
			_, binding, err := a.sessions.CurrentBound(ctx, a.scope(ctx))
			if err != nil {
				return err
			}
			if binding.SessionID() != next.ID {
				return errSessionBindingChanged
			}
			ctx = sessionpkg.WithBinding(ctx, binding)
		}
		row = next
		text = pending.Text
		ctx = withInboundTurnInput(ctx, pending)
	}
}

func (a *Agent) runChatTurnWithOutput(ctx context.Context, session *storage.Session, text string, out turnOutput) (*storage.Session, turn.Input, error) {
	ctx, release, err := a.enterTurn(ctx, session, out)
	if err != nil {
		return session, turn.Input{}, err
	}
	release()
	selection := a.modelSelectionForTurn(ctx, session)
	if a.turns.CanCompact(session.ID, turn.ExecutionFromContext(ctx)) && a.shouldCompact(ctx, session, selection) {
		next, content, err := a.compactSession(withInboundTurnInput(ctx, inboundTurnInput(ctx, text)), session, "auto", selection)
		if err != nil {
			return session, turn.Input{}, err
		}
		session = next
		ctx = a.executionContext(ctx)
		if !isBackgroundSession(session) {
			_, binding, err := a.sessions.CurrentBound(ctx, a.scope(ctx))
			if err != nil {
				return session, turn.Input{}, err
			}
			ctx = sessionpkg.WithBinding(ctx, binding)
		}
		_, _ = out.SendAssistant(ctx, content)
	}
	ctx, release, err = a.enterTurn(ctx, session, out)
	if err != nil {
		return session, turn.Input{}, err
	}
	attempt := storage.NewID()
	ctx = turn.WithAttempt(ctx, attempt)
	execution := turn.ExecutionFromContext(ctx)
	started := a.turns.StartExecution(session.ID, inboundTurnInput(ctx, text), execution, attempt)
	release()
	if !started {
		if a.turns.Execution(session.ID) == execution {
			return session, turn.Input{}, nil
		}
		return session, turn.Input{}, sessionpkg.ErrSessionBusy
	}
	defer a.turns.FinishRequest(session.ID, attempt)
	var pending turn.Input
	if err := a.runChat(ctx, session, text, out, selection, &pending); err != nil {
		if !a.turns.MatchesAttempt(session.ID, attempt) || a.turns.Snapshot(session.ID).Phase == turn.PhaseAwaitAppendConfirm {
			return session, turn.Input{}, nil
		}
		execution.Finish(err)
		a.turns.StopSession(session.ID, attempt)
		status := a.RuntimeStatus(session.ID)
		status.Phase = runtimestatus.PhaseError
		status.FinishedAt = storage.Now()
		status.Error = err.Error()
		out.PublishRuntimeStatus(ctx, status)
		return session, turn.Input{}, err
	}
	status := a.RuntimeStatus(session.ID)
	if status.Running() && (a.turns.Snapshot(session.ID).Phase == turn.PhaseIdle || a.turns.MatchesAttempt(session.ID, attempt)) {
		out.PublishRuntimeStatus(ctx, runtimeDoneStatus(status, storage.Now()))
	}
	return session, pending, nil
}

func (a *Agent) handleTurnContextDone(ctx context.Context, sessionID string, err error, out turnOutput) error {
	if a.turns.Execution(sessionID) != turn.ExecutionFromContext(ctx) || (a.turns.Snapshot(sessionID).Phase != turn.PhaseAwaitAppendConfirm && a.turns.MatchesAttempt(sessionID, turn.AttemptFromContext(ctx))) {
		if e := turn.ExecutionFromContext(ctx); e != nil {
			e.Finish(err)
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		if a.logger != nil {
			a.logger.WarnContext(ctx, "turn response timeout", "session_id", sessionID, "error", err.Error())
		}
		a.audit("turn_response_timeout", "session_id", sessionID, "error", err.Error())
		out.SendNotice(ctx, slog.LevelWarn, notificationrules.TurnTimeout)
	}
	return nil
}

func (a *Agent) runChat(ctx context.Context, session *storage.Session, text string, out turnOutput, selection modelmgr.Selection, completedPending *turn.Input) error {
	userSegments := a.materializeMedia(ctx, inboundSegments(ctx, text))
	userContent := llm.SegmentsContentText(userSegments)

	userMessage := &storage.Message{
		ID:                       storage.NewID(),
		SessionID:                session.ID,
		Role:                     storage.RoleUser,
		Content:                  userContent,
		Segments:                 storedMessageSegments(userSegments),
		ReplyToPlatformMessageID: inboundReplyMessageID(ctx),
	}
	if a.logger != nil {
		a.logger.Info("user input", "event", "user_message", "session_id", session.ID, "text", previewLogText(userContent))
	}

	loaded, err := a.contexts.Load(ctx, session.ID)
	if err != nil {
		return err
	}
	hasUserHistory := hasStorageUserMessage(loaded.Messages)
	compactSeedOnCurrentUser := false
	seed, err := contextmgr.PendingCompact(session)
	if err != nil {
		return err
	}
	if seed != nil {
		if !hasUserHistory {
			loaded.Summary = &storage.ContextSummary{Summary: seed.Summary}
			compactSeedOnCurrentUser = true
		} else {
			a.consumeContextCompactSeed(ctx, session)
		}
	}
	summaryOnCurrentUser := loaded.Summary != nil && !hasUserHistory && !compactSeedOnCurrentUser
	messages := append([]storage.Message{}, loaded.Messages...)
	messages = append(messages, *userMessage)

	locked, releaseRequest, err := a.sessions.EnterSessions(ctx, session.ID)
	if err != nil {
		return err
	}
	if !a.turns.MatchesAttempt(session.ID, turn.AttemptFromContext(ctx)) {
		releaseRequest()
		return context.Canceled
	}
	reqCtxInfo, reqCtx, done, err := a.requests.Start(locked, request.StartRequest{SessionID: session.ID, Kind: request.KindTurn, Label: "chat", Timeout: a.responseTimeout})
	releaseRequest()
	if err != nil {
		return err
	}
	defer done()
	reqCtx = withTurnRequestID(reqCtx, reqCtxInfo.ID)

	turnStartedAt := storage.Now()
	out.PublishRuntimeStatus(ctx, runtimestatus.Snapshot{SessionID: session.ID, Phase: runtimestatus.PhasePreparing, Provider: selection.Provider, Model: selection.Model, Mode: session.Mode, TurnStartedAt: turnStartedAt, StageStartedAt: turnStartedAt})
	scope := a.scope(ctx)
	llmMessages, err := a.promptBuilder.Build(ctx, PromptBuildRequest{Session: session, Scope: scope, Messages: messages, Summary: loaded.Summary})
	if err != nil {
		return err
	}
	tools, err := a.toolsForSession(ctx, session)
	if err != nil {
		return err
	}
	turnEvent, err := a.runHook(ctx, hook.Event{
		Point:   hook.PointLLMTurnPrepared,
		Session: hook.SessionContext{ID: session.ID},
		Message: hook.MessagePayload{ID: userMessage.ID, Role: string(llm.RoleUser), PlatformText: inboundTurnInput(ctx, text).PlatformText, Segments: append([]llm.MessageSegment(nil), userSegments...)},
		LLM: hook.LLMPayload{
			Provider: selection.Provider,
			Model:    selection.Model,
			Messages: llm.CloneMessages(llmMessages),
			Tools:    tools,
		},
	})
	if err != nil {
		return fmt.Errorf("llm turn hook: %w", err)
	}
	selection.Provider = turnEvent.LLM.Provider
	selection.Model = turnEvent.LLM.Model
	if session.Mode == storage.SessionModeWork || session.Mode == storage.SessionModeBackground {
		tools = turnEvent.LLM.Tools
	}
	out.PublishRuntimeStatus(ctx, runtimestatus.Snapshot{SessionID: session.ID, Phase: runtimestatus.PhasePreparing, Provider: selection.Provider, Model: selection.Model, Mode: session.Mode, TurnStartedAt: turnStartedAt, StageStartedAt: turnStartedAt})
	canonicalUserSegments := a.materializeMedia(ctx, turnEvent.Message.Segments)
	promptUserSegments := canonicalUserSegments
	if compactSeedOnCurrentUser || summaryOnCurrentUser {
		promptUserSegments = llm.PrependSegmentText(promptUserSegments, summaryUserPrefix(loaded.Summary.Summary))
	}
	llmMessages = llm.SetLatestUserSegments(llmMessages, promptUserSegments)
	if compactSeedOnCurrentUser {
		userMessage.Content = llm.SegmentsContentText(promptUserSegments)
		userMessage.Segments = storedMessageSegments(promptUserSegments)
	} else {
		userMessage.Content = llm.SegmentsContentText(canonicalUserSegments)
		userMessage.Segments = storedMessageSegments(canonicalUserSegments)
	}
	if err := a.persistTurnMessage(ctx, userMessage, "append_user_message"); err != nil {
		return err
	}
	if compactSeedOnCurrentUser {
		a.consumeContextCompactSeed(ctx, session)
	}

	bufferOutput := bufferAssistantOutput(ctx)
	var finalText string
	var finalRawText string
	var platformFinalText string
	var finalStream delivery.MessageStream
	var finalReceipt delivery.Receipt
	var deferredOutputs []delivery.Output
	var usage *llm.Usage
	toolRounds := 0
	inToolPhase := false
	foregroundPrepared := false
	for {
		var refreshErr error
		reqCtx, refreshErr = a.refreshExecution(reqCtx, session)
		if refreshErr != nil {
			return refreshErr
		}
		ctx = a.executionContext(ctx)
		if sessionpkg.WasPromoted(session) && !foregroundPrepared {
			foregroundPrepared = true
			scope := a.scope(reqCtx)
			prompt, err := a.promptBuilder.Build(reqCtx, PromptBuildRequest{Session: session, Scope: scope})
			if err != nil {
				return err
			}
			updated := make([]llm.LLMMessage, 0, len(llmMessages)+len(prompt))
			for _, message := range prompt {
				if message.Role == llm.RoleSystem {
					updated = append(updated, message)
				}
			}
			for _, message := range llmMessages {
				if message.Role != llm.RoleSystem {
					updated = append(updated, message)
				}
			}
			llmMessages = updated
			selection = a.modelSelectionForTurn(reqCtx, session)
			llmMessages = withForegroundInstructions(llmMessages)
			tools, err = a.toolsForSession(reqCtx, session)
			if err != nil {
				return err
			}
		}
		var pending *pendingUserMessage
		if inToolPhase {
			llmMessages, pending = a.drainPendingUserInput(session.ID, llmMessages, turn.AttemptFromContext(ctx))
		}
		stream := out.StartStream(reqCtx)
		llmStageStartedAt := storage.Now()
		out.PublishRuntimeStatus(ctx, runtimestatus.Snapshot{SessionID: session.ID, Phase: runtimestatus.PhaseLLM, Provider: selection.Provider, Model: selection.Model, Mode: session.Mode, RequestID: reqCtxInfo.ID, Kind: request.KindTurn, Label: "chat", TurnStartedAt: turnStartedAt, StageStartedAt: llmStageStartedAt, Usage: usage})
		result, err := a.callLLM(reqCtx, session, selection, llmMessages, tools, pending, stream, out)
		if len(result.Messages) > 0 {
			llmMessages = result.Messages
		}
		if err != nil {
			return err
		}
		streaming := result.Stream != nil
		if err := reqCtx.Err(); err != nil {
			return a.handleTurnContextDone(ctx, session.ID, err, out)
		}
		assistantText := result.Text
		assistantRawText := result.RawText
		if result.Usage != nil {
			usage = result.Usage
		}
		out.PublishRuntimeStatus(ctx, runtimestatus.Snapshot{SessionID: session.ID, Phase: runtimestatus.PhaseLLM, Provider: selection.Provider, Model: selection.Model, Mode: session.Mode, RequestID: reqCtxInfo.ID, Kind: request.KindTurn, Label: "chat", TurnStartedAt: turnStartedAt, StageStartedAt: llmStageStartedAt, Usage: usage})
		immediateOutputs, laterOutputs := delivery.SplitByDeliveryTiming(result.Outputs)
		if len(result.ToolCalls) == 0 {
			deferredOutputs = append(deferredOutputs, laterOutputs...)
		}
		if err := out.SendOutputs(ctx, immediateOutputs); err != nil {
			return err
		}
		if len(result.ToolCalls) == 0 {
			finalText = joinAssistantText(finalText, assistantRawText)
			finalRawText = joinAssistantText(finalRawText, assistantRawText)
			platformFinalText = joinAssistantText(platformFinalText, assistantText)
			finalStream = result.Stream
			break
		}
		if err := out.FinishIntermediate(ctx, reqCtx, result.Stream, assistantText, streaming); err != nil {
			return err
		}
		if err := out.SendOutputs(ctx, laterOutputs); err != nil {
			return err
		}
		if !inToolPhase {
			if !a.turns.StartToolPhase(session.ID, turn.AttemptFromContext(ctx)) {
				return nil
			}
			inToolPhase = true
		}
		assistantToolCallIndex := len(llmMessages)
		llmMessages = append(llmMessages, llm.LLMMessage{Role: llm.RoleAssistant, Segments: llm.TextSegments(assistantRawText), ToolCalls: result.ToolCalls})
		if toolRounds >= a.maxToolRoundsPerTurn() {
			out.SendPreview(ctx, fmt.Sprintf("已达到 max_rounds_per_turn=%d，后续工具调用未执行，正在请求模型总结当前进度。", a.maxToolRoundsPerTurn()))
			llmMessages = append(llmMessages, skippedToolMessages(result.ToolCalls, a.maxToolRoundsPerTurn())...)
			var summaryPending *pendingUserMessage
			llmMessages, summaryPending = a.drainPendingUserInput(session.ID, llmMessages, turn.AttemptFromContext(ctx))
			llmMessages = append(llmMessages, llm.LLMMessage{Role: llm.RoleUser, Segments: llm.TextSegments("工具调用轮次已达到上限，可以询问用户是否继续或者基于已有工具结果和当前上下文总结当前进度。")})
			tools = nil
			stream := out.StartStream(reqCtx)
			summary, err := a.callLLM(reqCtx, session, selection, llmMessages, tools, summaryPending, stream, out)
			if err != nil {
				return err
			}
			if len(summary.ToolCalls) > 0 {
				// TODO: 后续支持强制 tool_choice=none；当前总结请求已不传 tools，若仍返回工具调用则忽略。
				out.SendPreview(ctx, "总结请求仍返回了工具调用，已忽略。")
			}
			immediateOutputs, laterOutputs := delivery.SplitByDeliveryTiming(summary.Outputs)
			deferredOutputs = append(deferredOutputs, laterOutputs...)
			if err := out.SendOutputs(ctx, immediateOutputs); err != nil {
				return err
			}
			summaryText := summary.Text
			summaryRawText := summary.RawText
			if summaryText == "" {
				summaryText = "工具调用轮次已达到上限，当前流程已停止。"
				if summaryRawText == "" {
					summaryRawText = summaryText
				}
			}
			if summary.Usage != nil {
				usage = summary.Usage
			}
			finalText = joinAssistantText(finalText, summaryRawText)
			finalRawText = joinAssistantText(finalRawText, summaryRawText)
			platformFinalText = joinAssistantText(platformFinalText, summaryText)
			finalStream = summary.Stream
			break
		}
		toolRounds++
		execution := a.executeToolCalls(reqCtx, session, result.ToolCalls, assistantRawText, assistantRawText, out)
		if execution.Stopped {
			if err := reqCtx.Err(); err != nil {
				return a.handleTurnContextDone(ctx, session.ID, err, out)
			}
			return nil
		}
		llmMessages[assistantToolCallIndex].ToolCalls = append([]llm.ToolCallRequest(nil), execution.PreparedCalls...)
		llmMessages = append(llmMessages, execution.Messages...)
		if err := a.persistTurnMessages(ctx, session.ID, "append_tool_transcript", execution.Transcript); err != nil {
			return err
		}
		tools, err = a.toolsForSession(ctx, session)
		if err != nil {
			return err
		}
		if execution.ConfirmationExtra != "" {
			llmMessages = append(llmMessages, llm.LLMMessage{Role: llm.RoleUser, Segments: llm.TextSegments("补充：" + execution.ConfirmationExtra)})
		}
	}
	if err := reqCtx.Err(); err != nil {
		return a.handleTurnContextDone(ctx, session.ID, err, out)
	}
	platformOutputText := platformFinalText
	var finalSendErr error
	backgroundOutput := isBackgroundSession(session)
	emptyAssistantResponse := strings.TrimSpace(platformOutputText) == "" && strings.TrimSpace(finalText) == "" && len(deferredOutputs) == 0
	if emptyAssistantResponse && !backgroundOutput {
		platformOutputText = "模型这次没有返回可见内容。"
	}
	out.PublishRuntimeStatus(ctx, runtimestatus.Snapshot{SessionID: session.ID, Phase: runtimestatus.PhaseSending, Provider: selection.Provider, Model: selection.Model, Mode: session.Mode, RequestID: reqCtxInfo.ID, Kind: request.KindTurn, Label: "chat", TurnStartedAt: turnStartedAt, StageStartedAt: storage.Now()})
	if strings.TrimSpace(platformOutputText) != "" {
		var err error
		platformOutputText, err = a.prepareAssistantOutput(ctx, hook.PointAgentTurnOutputPrepared, platformOutputText)
		if err != nil {
			return fmt.Errorf("turn output hook: %w", err)
		}
		if !bufferOutput {
			if finalStream != nil {
				finalReceipt, finalSendErr = out.ReplaceAndFinishStream(ctx, reqCtx, finalStream, platformOutputText)
			} else {
				finalReceipt, finalSendErr = out.SendAssistant(ctx, platformOutputText)
			}
			if finalSendErr != nil && len(finalReceipt.PlatformMessageIDs) == 0 {
				return finalSendErr
			}
		}
	}

	if !bufferOutput && finalSendErr == nil {
		finalSendErr = out.SendOutputs(ctx, deferredOutputs)
		if finalSendErr != nil && len(finalReceipt.PlatformMessageIDs) == 0 {
			return finalSendErr
		}
	}

	// 工具调用消息按 OpenAI messages 形态保存；discover 结果持久化时会压缩 schema，避免历史上下文膨胀。
	assistantMessage := &storage.Message{
		SessionID: session.ID,
		Role:      storage.RoleAssistant,
		Content:   finalText,
		Metadata:  assistantRawTextMetadata(finalText, finalRawText),
	}
	persistedAssistant := false
	if !emptyAssistantResponse {
		if err := a.persistTurnMessage(ctx, assistantMessage, "append_assistant_message"); err != nil {
			return err
		}
		persistedAssistant = true
	}
	if bufferOutput {
		if strings.TrimSpace(platformOutputText) != "" {
			receipt, err := out.SendAssistant(ctx, platformOutputText)
			if persistedAssistant {
				a.mapSentAssistantMessage(ctx, session.ID, assistantMessage.ID, receipt)
			}
			if err != nil {
				a.audit("platform_send_error", "session_id", session.ID, "operation", "send_assistant_message", "error", err.Error())
				return err
			}
		}
		if err := out.SendOutputs(ctx, deferredOutputs); err != nil {
			return err
		}
	} else if persistedAssistant {
		a.mapSentAssistantMessage(ctx, session.ID, assistantMessage.ID, finalReceipt)
	}
	if finalSendErr != nil {
		return finalSendErr
	}
	if err := a.sessions.Touch(ctx, session); err != nil {
		a.audit("persistence_error", "session_id", session.ID, "operation", "touch_session", "error", err.Error())
		return err
	}
	a.recordUsage(session.ID, usage)
	doneStatus := runtimeDoneStatus(runtimestatus.Snapshot{SessionID: session.ID, Provider: selection.Provider, Model: selection.Model, Mode: session.Mode, TurnStartedAt: turnStartedAt, StageStartedAt: turnStartedAt, Usage: usage}, storage.Now())
	out.PublishRuntimeStatus(ctx, doneStatus)
	nextSelection := a.modelSelectionForTurn(ctx, session)
	if a.shouldCompact(ctx, session, nextSelection) {
		_, _ = out.SendAssistant(ctx, "compact status: will compact before next request")
	}
	pending, completed := a.turns.CompleteLLMInput(session.ID, turn.AttemptFromContext(ctx))
	if !completed {
		return nil
	}
	if completedPending != nil {
		*completedPending = pending
	}
	if execution := turn.ExecutionFromContext(ctx); execution != nil {
		execution.SetResult(session.ID, assistantMessage.ID, finalRawText)
	}
	a.sessions.MaybeScheduleNaming(ctx, session.ID)
	return nil
}

func (a *Agent) modelSelectionForTurn(ctx context.Context, session *storage.Session) modelmgr.Selection {
	mode := storage.SessionModeWork
	if session != nil && session.Mode != "" && session.Mode != storage.SessionModeBackground {
		mode = session.Mode
	}
	selection := a.models.ResolveMode(mode).ModelSelection
	if override, ok := ctx.Value(backgroundModelSelectionKey{}).(config.ModelSelection); ok {
		if override.Provider != "" {
			selection.Provider = override.Provider
		}
		if override.Model != "" {
			selection.Model = override.Model
		}
	}
	return a.models.Resolve(selection)
}

func hasStorageUserMessage(messages []storage.Message) bool {
	for _, message := range messages {
		if message.Role == storage.RoleUser {
			return true
		}
	}
	return false
}
