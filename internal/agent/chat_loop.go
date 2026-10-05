package agent

import (
	"fmt"

	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/request"
	runtimestatus "elbot/internal/runtime"
	sessionpkg "elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

type chatLoopResult struct {
	Outcome           chatTurnOutcome
	Err               error
	QuietCancellation bool
	Commit            replyCommitInput
}

func failedChatLoop(err error) chatLoopResult {
	return chatLoopResult{Outcome: failedChatOutcome(err), Err: err}
}

func (r *chatRunner) runLoop(s *chatTurnState) chatLoopResult {
	var finalText string
	var finalRawText string
	var platformFinalText string
	var finalStream delivery.MessageStream
	var deferredOutputs []delivery.Output
	toolRounds := 0
	inToolPhase := false
	foregroundPrepared := false
	for {
		var refreshErr error
		s.requestCtx, refreshErr = r.view.RefreshSession(s.requestCtx, s.session)
		if refreshErr != nil {
			return failedChatLoop(refreshErr)
		}
		s.ctx = r.view.Context(s.ctx)
		if sessionpkg.WasPromoted(s.session) && !foregroundPrepared {
			foregroundPrepared = true
			scope := r.identity.Scope(s.requestCtx)
			prompt, err := r.promptBuilder.Build(s.requestCtx, PromptBuildRequest{Session: s.session, Scope: scope})
			if err != nil {
				return failedChatLoop(err)
			}
			updated := make([]llm.LLMMessage, 0, len(s.messages)+len(prompt))
			for _, message := range prompt {
				if message.Role == llm.RoleSystem {
					updated = append(updated, message)
				}
			}
			for _, message := range s.messages {
				if message.Role != llm.RoleSystem {
					updated = append(updated, message)
				}
			}
			s.messages = updated
			s.selection = modelSelectionForTurn(s.requestCtx, r.models, s.session)
			s.messages = withForegroundInstructions(s.messages)
			s.tools, err = r.toolsForSession(s.requestCtx, s.session)
			if err != nil {
				return failedChatLoop(err)
			}
		}
		var pending *pendingUserMessage
		if inToolPhase {
			s.messages, pending = r.drainPendingUserInput(s.session.ID, s.messages, turn.AttemptFromContext(s.ctx))
		}
		stream := s.output.StartStream(s.requestCtx)
		llmStageStartedAt := storage.Now()
		s.output.PublishRuntimeStatus(s.ctx, runtimestatus.Snapshot{SessionID: s.session.ID, Phase: runtimestatus.PhaseLLM, Provider: s.selection.Provider, Model: s.selection.Model, Mode: s.session.Mode, RequestID: s.requestID, Kind: request.KindTurn, Label: "chat", TurnStartedAt: s.startedAt, StageStartedAt: llmStageStartedAt, Usage: s.usage})
		result, err := r.caller.Call(s.requestCtx, s.session, s.selection, s.messages, s.tools, pending, stream, s.output)
		if len(result.Messages) > 0 {
			s.messages = result.Messages
		}
		if err != nil {
			return failedChatLoop(err)
		}
		streaming := result.Stream != nil
		if err := s.requestCtx.Err(); err != nil {
			return chatLoopResult{Outcome: chatTurnCanceled, Err: err, QuietCancellation: true}
		}
		assistantText := result.Text
		assistantRawText := result.RawText
		if result.Usage != nil {
			s.usage = result.Usage
		}
		s.output.PublishRuntimeStatus(s.ctx, runtimestatus.Snapshot{SessionID: s.session.ID, Phase: runtimestatus.PhaseLLM, Provider: s.selection.Provider, Model: s.selection.Model, Mode: s.session.Mode, RequestID: s.requestID, Kind: request.KindTurn, Label: "chat", TurnStartedAt: s.startedAt, StageStartedAt: llmStageStartedAt, Usage: s.usage})
		immediateOutputs, laterOutputs := delivery.SplitByDeliveryTiming(result.Outputs)
		if len(result.ToolCalls) == 0 {
			deferredOutputs = append(deferredOutputs, laterOutputs...)
		}
		if err := s.output.SendOutputs(s.ctx, immediateOutputs); err != nil {
			return failedChatLoop(err)
		}
		if len(result.ToolCalls) == 0 {
			finalText = joinAssistantText(finalText, assistantRawText)
			finalRawText = joinAssistantText(finalRawText, assistantRawText)
			platformFinalText = joinAssistantText(platformFinalText, assistantText)
			finalStream = result.Stream
			break
		}
		if err := s.output.FinishIntermediate(s.ctx, s.requestCtx, result.Stream, assistantText, streaming); err != nil {
			return failedChatLoop(err)
		}
		if err := s.output.SendOutputs(s.ctx, laterOutputs); err != nil {
			return failedChatLoop(err)
		}
		if !inToolPhase {
			if !r.turns.StartToolPhase(s.session.ID, turn.AttemptFromContext(s.ctx)) {
				return chatLoopResult{Outcome: chatTurnStopped}
			}
			inToolPhase = true
		}
		assistantToolCallIndex := len(s.messages)
		s.messages = append(s.messages, llm.LLMMessage{Role: llm.RoleAssistant, Segments: llm.TextSegments(assistantRawText), ToolCalls: result.ToolCalls})
		if toolRounds >= r.maxToolRoundsPerTurn() {
			s.output.SendPreview(s.ctx, fmt.Sprintf("已达到 max_rounds_per_turn=%d，后续工具调用未执行，正在请求模型总结当前进度。", r.maxToolRoundsPerTurn()))
			s.messages = append(s.messages, skippedToolMessages(result.ToolCalls, r.maxToolRoundsPerTurn())...)
			var summaryPending *pendingUserMessage
			s.messages, summaryPending = r.drainPendingUserInput(s.session.ID, s.messages, turn.AttemptFromContext(s.ctx))
			s.messages = append(s.messages, llm.LLMMessage{Role: llm.RoleUser, Segments: llm.TextSegments("工具调用轮次已达到上限，可以询问用户是否继续或者基于已有工具结果和当前上下文总结当前进度。")})
			s.tools = nil
			stream := s.output.StartStream(s.requestCtx)
			summary, err := r.caller.Call(s.requestCtx, s.session, s.selection, s.messages, s.tools, summaryPending, stream, s.output)
			if err != nil {
				return failedChatLoop(err)
			}
			if len(summary.ToolCalls) > 0 {
				// TODO: 后续支持强制 tool_choice=none；当前总结请求已不传 tools，若仍返回工具调用则忽略。
				s.output.SendPreview(s.ctx, "总结请求仍返回了工具调用，已忽略。")
			}
			immediateOutputs, laterOutputs := delivery.SplitByDeliveryTiming(summary.Outputs)
			deferredOutputs = append(deferredOutputs, laterOutputs...)
			if err := s.output.SendOutputs(s.ctx, immediateOutputs); err != nil {
				return failedChatLoop(err)
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
				s.usage = summary.Usage
			}
			finalText = joinAssistantText(finalText, summaryRawText)
			finalRawText = joinAssistantText(finalRawText, summaryRawText)
			platformFinalText = joinAssistantText(platformFinalText, summaryText)
			finalStream = summary.Stream
			break
		}
		toolRounds++
		execution := r.executeToolCalls(s.requestCtx, s.session, result.ToolCalls, assistantRawText, assistantRawText, s.output)
		if execution.Stopped {
			if err := s.requestCtx.Err(); err != nil {
				return chatLoopResult{Outcome: chatTurnCanceled, Err: err, QuietCancellation: true}
			}
			return chatLoopResult{Outcome: chatTurnStopped}
		}
		s.messages[assistantToolCallIndex].ToolCalls = append([]llm.ToolCallRequest(nil), execution.PreparedCalls...)
		s.messages = append(s.messages, execution.Messages...)
		if err := persistTurnMessages(s.ctx, r.messages, r.media, r.persistenceFailed, s.session.ID, "append_tool_transcript", execution.Transcript); err != nil {
			return failedChatLoop(err)
		}
		s.tools, err = r.toolsForSession(s.ctx, s.session)
		if err != nil {
			return failedChatLoop(err)
		}
		if execution.ConfirmationExtra != "" {
			s.messages = append(s.messages, llm.LLMMessage{Role: llm.RoleUser, Segments: llm.TextSegments("补充：" + execution.ConfirmationExtra)})
		}
	}
	if err := s.requestCtx.Err(); err != nil {
		return chatLoopResult{Outcome: chatTurnCanceled, Err: err, QuietCancellation: true}
	}

	return chatLoopResult{
		Outcome: chatTurnCompleted,
		Commit:  replyCommitInput{Session: s.session, Text: finalText, RawText: finalRawText, PlatformText: platformFinalText, Stream: finalStream, Outputs: deferredOutputs},
	}
}
