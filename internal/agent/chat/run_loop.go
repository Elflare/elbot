package chat

import (
	"context"
	"fmt"

	"elbot/internal/agent/dialogue"
	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/request"
	runtimestatus "elbot/internal/runtime"
	sessionpkg "elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

func (p *preparedLoop) RunLoop(ctx, requestCtx context.Context, in dialogue.LoopInput, out dialogue.Output) (result dialogue.LoopResult) {
	r, s := p.route, p.state
	if p.compactSeedOnCurrentUser {
		r.consumeContextCompactSeed(s.requestCtx, s.session)
	}
	defer func() { result.Usage, result.Selection = s.usage, s.selection }()
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
		s.requestCtx, refreshErr = r.View.RefreshSession(s.requestCtx, s.session)
		if refreshErr != nil {
			return failedLoop(refreshErr)
		}
		s.ctx = r.View.Context(s.ctx)
		if sessionpkg.WasPromoted(s.session) && !foregroundPrepared {
			foregroundPrepared = true
			s.selection = modelmgr.SelectionForTurn(s.requestCtx, r.Models, s.session)
			var modelErr error
			s.requestCtx, modelErr = r.View.WithModel(s.requestCtx, s.selection)
			if modelErr == nil {
				s.ctx, modelErr = r.View.WithModel(s.ctx, s.selection)
			}
			if modelErr != nil {
				return failedLoop(modelErr)
			}
			scope := r.Preparer.Identity.Scope(s.requestCtx)
			prompt, err := r.PromptBuilder.Build(s.requestCtx, PromptBuildRequest{Session: s.session, Scope: scope})
			if err != nil {
				return failedLoop(err)
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
			s.messages = withForegroundInstructions(s.messages)
			s.tools, err = r.Tools.Schemas(s.requestCtx, s.session)
			if err != nil {
				return failedLoop(err)
			}
		}
		var pending *dialogue.PendingUserMessage
		if inToolPhase {
			s.messages, pending = drainPendingUserInput(r.Turns, s.session.ID, s.messages, turn.AttemptFromContext(s.ctx))
		}
		stream := s.output.StartStream(s.requestCtx)
		llmStageStartedAt := storage.Now()
		s.output.PublishRuntimeStatus(s.ctx, runtimestatus.Snapshot{SessionID: s.session.ID, Phase: runtimestatus.PhaseLLM, Provider: s.selection.Provider, Model: s.selection.Model, Mode: s.session.Mode, RequestID: s.requestID, Kind: request.KindTurn, Label: "chat", TurnStartedAt: s.startedAt, StageStartedAt: llmStageStartedAt, Usage: s.usage})
		result, err := r.Caller.Call(s.requestCtx, s.session, s.selection, s.messages, s.tools, pending, stream, s.output)
		if len(result.Messages) > 0 {
			s.messages = result.Messages
		}
		if err != nil {
			return failedLoop(err)
		}
		streaming := result.Stream != nil
		if err := s.requestCtx.Err(); err != nil {
			return dialogue.LoopResult{Outcome: dialogue.Canceled, Err: err, QuietCancellation: true}
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
			return failedLoop(err)
		}
		if len(result.ToolCalls) == 0 {
			finalText = joinAssistantText(finalText, assistantRawText)
			finalRawText = joinAssistantText(finalRawText, assistantRawText)
			platformFinalText = joinAssistantText(platformFinalText, assistantText)
			finalStream = result.Stream
			break
		}
		if err := s.output.FinishIntermediate(s.ctx, s.requestCtx, result.Stream, assistantText, streaming); err != nil {
			return failedLoop(err)
		}
		if err := s.output.SendOutputs(s.ctx, laterOutputs); err != nil {
			return failedLoop(err)
		}
		if !inToolPhase {
			if !r.Turns.StartToolPhase(s.session.ID, turn.AttemptFromContext(s.ctx)) {
				return dialogue.LoopResult{Outcome: dialogue.Stopped}
			}
			inToolPhase = true
		}
		assistantToolCallIndex := len(s.messages)
		s.messages = append(s.messages, llm.LLMMessage{Role: llm.RoleAssistant, Segments: llm.TextSegments(assistantRawText), ToolCalls: result.ToolCalls})
		if toolRounds >= r.Tools.MaxRoundsPerTurn() {
			s.output.SendPreview(s.ctx, fmt.Sprintf("已达到 max_rounds_per_turn=%d，后续工具调用未执行，正在请求模型总结当前进度。", r.Tools.MaxRoundsPerTurn()))
			s.messages = append(s.messages, skippedToolMessages(result.ToolCalls, r.Tools.MaxRoundsPerTurn())...)
			var summaryPending *dialogue.PendingUserMessage
			s.messages, summaryPending = drainPendingUserInput(r.Turns, s.session.ID, s.messages, turn.AttemptFromContext(s.ctx))
			s.messages = append(s.messages, llm.LLMMessage{Role: llm.RoleUser, Segments: llm.TextSegments("工具调用轮次已达到上限，可以询问用户是否继续或者基于已有工具结果和当前上下文总结当前进度。")})
			s.tools = nil
			stream := s.output.StartStream(s.requestCtx)
			summary, err := r.Caller.Call(s.requestCtx, s.session, s.selection, s.messages, s.tools, summaryPending, stream, s.output)
			if err != nil {
				return failedLoop(err)
			}
			if len(summary.ToolCalls) > 0 {
				// TODO: 后续支持强制 tool_choice=none；当前总结请求已不传 tools，若仍返回工具调用则忽略。
				s.output.SendPreview(s.ctx, "总结请求仍返回了工具调用，已忽略。")
			}
			immediateOutputs, laterOutputs := delivery.SplitByDeliveryTiming(summary.Outputs)
			deferredOutputs = append(deferredOutputs, laterOutputs...)
			if err := s.output.SendOutputs(s.ctx, immediateOutputs); err != nil {
				return failedLoop(err)
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
		execution := r.Tools.Execute(s.requestCtx, s.session, result.ToolCalls, assistantRawText, assistantRawText, s.output)
		if execution.Stopped {
			if err := s.requestCtx.Err(); err != nil {
				return dialogue.LoopResult{Outcome: dialogue.Canceled, Err: err, QuietCancellation: true}
			}
			return dialogue.LoopResult{Outcome: dialogue.Stopped}
		}
		s.messages[assistantToolCallIndex].ToolCalls = append([]llm.ToolCallRequest(nil), execution.PreparedCalls...)
		s.messages = append(s.messages, execution.Messages...)
		if err := r.Messages.AppendTranscript(s.ctx, s.session.ID, execution.Transcript); err != nil {
			return failedLoop(err)
		}
		s.tools, err = r.Tools.Schemas(s.ctx, s.session)
		if err != nil {
			return failedLoop(err)
		}
		if execution.ConfirmationExtra != "" {
			s.messages = append(s.messages, llm.LLMMessage{Role: llm.RoleUser, Segments: llm.TextSegments("补充：" + execution.ConfirmationExtra)})
		}
	}
	if err := s.requestCtx.Err(); err != nil {
		return dialogue.LoopResult{Outcome: dialogue.Canceled, Err: err, QuietCancellation: true}
	}

	return dialogue.LoopResult{
		Outcome: dialogue.Completed,
		Commit:  dialogue.ReplyCommitInput{Session: s.session, Text: finalText, RawText: finalRawText, PlatformText: platformFinalText, Stream: finalStream, Outputs: deferredOutputs},
	}
}

func failedLoop(err error) dialogue.LoopResult {
	return dialogue.LoopResult{Outcome: dialogue.FailedOutcome(err), Err: err}
}
