package chat

import (
	agentshare "elbot/internal/agent/share"

	"context"
	"fmt"

	"elbot/internal/delivery"
	"elbot/internal/llm"

	"elbot/internal/request"
	runtimestatus "elbot/internal/runtime"
	sessionpkg "elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

func (p *preparedLoop) RunLoop(ctx, requestCtx context.Context, in agentshare.LoopInput, out agentshare.Output) (result agentshare.LoopResult) {
	r, s := p.route, p.state
	for i := range p.materials.Loaded.Messages {
		head := &p.materials.Loaded.Messages[i]
		fields, err := storage.DecodeSessionMetadata(head.Metadata)
		if err != nil {
			return failedLoop(err)
		}
		if string(fields["tool_batch_status"]) != `"pending"` {
			continue
		}
		_ = fields.Set("tool_batch_status", "interrupted")
		head.Metadata, _ = fields.Encode()
		if _, err := r.Messages.Commit(s.requestCtx, storage.DialogueCommit{SessionID: s.session.ID, ToolResult: &storage.ToolResultCommit{Call: head}}, "close_interrupted_tools"); err != nil {
			return failedLoop(err)
		}
	}
	if p.compactSeedOnCurrentUser {
		if err := r.consumeContextCompactSeed(s.requestCtx, s.session); err != nil {
			return failedLoop(err)
		}
	}
	defer func() {
		result.Usage, result.Selection = s.usage, s.selection
	}()
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
			s.selection = r.SelectionForTurn(s.requestCtx, s.session)
			if err := r.CheckSelection(s.selection); err != nil {
				return failedLoop(err)
			}
			var modelErr error
			s.requestCtx, modelErr = r.View.WithModel(s.requestCtx, s.selection)
			if modelErr == nil {
				s.ctx, modelErr = r.View.WithModel(s.ctx, s.selection)
			}
			if modelErr != nil {
				return failedLoop(modelErr)
			}
			scope := r.Identity.Scope(s.requestCtx)
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
			s.tools, err = r.Tools.Schemas(s.requestCtx, s.session)
			if err != nil {
				return failedLoop(err)
			}
		}
		if err := r.View.CheckSelection(s.session, s.selection); err != nil {
			return failedLoop(err)
		}
		var pending *agentshare.PendingUserMessage
		if inToolPhase {
			s.messages, pending = drainPendingUserInput(r.Turns, s.session.ID, s.messages, turn.AttemptFromContext(s.ctx))
		}
		stream, err := s.output.StartStream(s.requestCtx)
		if err != nil {
			return failedLoop(err)
		}
		llmStageStartedAt := storage.Now()
		r.Status.Record(s.ctx, runtimestatus.Snapshot{
			SessionID:      s.session.ID,
			Phase:          runtimestatus.PhaseLLM,
			Provider:       s.selection.Provider,
			Model:          s.selection.Model,
			Mode:           s.session.Mode,
			RequestID:      s.requestID,
			Kind:           request.KindTurn,
			Label:          "chat",
			TurnStartedAt:  s.startedAt,
			StageStartedAt: llmStageStartedAt,
			Usage:          s.usage,
		})
		result, err := r.Caller.Call(s.requestCtx, s.session, s.selection, s.messages, s.tools, pending, stream, s.output)
		if len(result.Messages) > 0 {
			s.messages = result.Messages
		}
		if err != nil {
			return failedLoop(err)
		}
		streaming := result.Stream != nil
		if err := s.requestCtx.Err(); err != nil {
			return agentshare.LoopResult{Outcome: agentshare.Canceled, Err: err, QuietCancellation: true}
		}
		assistantText := result.Text
		assistantRawText := result.RawText
		if result.Usage != nil {
			s.usage = result.Usage
		}
		r.Status.Record(s.ctx, runtimestatus.Snapshot{
			SessionID:      s.session.ID,
			Phase:          runtimestatus.PhaseLLM,
			Provider:       s.selection.Provider,
			Model:          s.selection.Model,
			Mode:           s.session.Mode,
			RequestID:      s.requestID,
			Kind:           request.KindTurn,
			Label:          "chat",
			TurnStartedAt:  s.startedAt,
			StageStartedAt: llmStageStartedAt,
			Usage:          s.usage,
		})
		immediateOutputs, laterOutputs := delivery.SplitByDeliveryTiming(result.Outputs)
		if len(result.ToolCalls) == 0 {
			deferredOutputs = append(deferredOutputs, laterOutputs...)
		}
		if _, err := s.output.SendOutputs(s.ctx, immediateOutputs); err != nil {
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
		if _, err := s.output.SendOutputs(s.ctx, laterOutputs); err != nil {
			return failedLoop(err)
		}
		if !inToolPhase {
			if !r.Turns.StartToolPhase(s.session.ID, turn.AttemptFromContext(s.ctx)) {
				return agentshare.LoopResult{Outcome: agentshare.Stopped}
			}
			inToolPhase = true
		}
		if toolRounds >= r.Tools.MaxRoundsPerTurn() {
			s.output.SendPreview(s.ctx, fmt.Sprintf("max_rounds_per_turn=%d → 跳过后续工具，生成总结", r.Tools.MaxRoundsPerTurn()))
			skipped := skippedToolMessages(result.ToolCalls, r.Tools.MaxRoundsPerTurn())
			committer := r.NewToolTranscriptCommitter()
			head := agentshare.ToolCallStorageMessage(s.session.ID, assistantRawText, assistantRawText, result.ToolCalls)
			if _, err := committer.Begin(s.requestCtx, &head); err != nil {
				return failedLoop(err)
			}
			for i, message := range skipped {
				stored := agentshare.ToolResultStorageMessage(s.session.ID, message)
				if _, err := committer.Result(s.requestCtx, i, result.ToolCalls[i], message, &storage.ToolResultCommit{Result: &stored}); err != nil {
					return failedLoop(err)
				}
			}
			if _, err := committer.Flush(s.requestCtx); err != nil {
				return failedLoop(err)
			}
			s.messages = append(s.messages, committer.Committed()...)
			var summaryPending *agentshare.PendingUserMessage
			s.messages, summaryPending = drainPendingUserInput(r.Turns, s.session.ID, s.messages, turn.AttemptFromContext(s.ctx))
			summaryInput := &storage.Message{SessionID: s.session.ID, Role: storage.RoleUser, Content: "工具调用轮次已达到上限，可以询问用户是否继续或者基于已有工具结果和当前上下文总结当前进度。"}
			if _, err := r.Messages.Append(s.requestCtx, summaryInput, "append_summary_input"); err != nil {
				return failedLoop(err)
			}
			projected, err := agentshare.MessageFromStorage(*summaryInput)
			if err != nil {
				return failedLoop(err)
			}
			s.messages = append(s.messages, projected)
			s.tools = nil
			stream, err := s.output.StartStream(s.requestCtx)
			if err != nil {
				return failedLoop(err)
			}
			summary, err := r.Caller.Call(s.requestCtx, s.session, s.selection, s.messages, s.tools, summaryPending, stream, s.output)
			if err != nil {
				return failedLoop(err)
			}
			if len(summary.ToolCalls) > 0 {
				// TODO: 后续支持强制 tool_choice=none；当前总结请求已不传 tools，若仍返回工具调用则忽略。
				s.output.SendPreview(s.ctx, "总结中的工具调用已忽略")
			}
			immediateOutputs, laterOutputs := delivery.SplitByDeliveryTiming(summary.Outputs)
			deferredOutputs = append(deferredOutputs, laterOutputs...)
			if _, err := s.output.SendOutputs(s.ctx, immediateOutputs); err != nil {
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
		committer := r.NewToolTranscriptCommitter()
		execution := r.Tools.ExecuteWithCommitter(s.requestCtx, s.session, result.ToolCalls, assistantRawText, assistantRawText, s.output, committer)
		if execution.Err != nil {
			return failedLoop(execution.Err)
		}
		if execution.Stopped {
			if err := s.requestCtx.Err(); err != nil {
				return agentshare.LoopResult{Outcome: agentshare.Canceled, Err: err, QuietCancellation: true}
			}
			return agentshare.LoopResult{Outcome: agentshare.Stopped}
		}
		s.messages = append(s.messages, committer.Committed()...)
		s.tools, err = r.Tools.Schemas(s.ctx, s.session)
		if err != nil {
			return failedLoop(err)
		}
		if execution.ConfirmationExtra != "" {
			message := &storage.Message{SessionID: s.session.ID, Role: storage.RoleUser, Content: "补充：" + execution.ConfirmationExtra}
			if _, err := r.Messages.Append(s.requestCtx, message, "append_confirmation_input"); err != nil {
				return failedLoop(err)
			}
			projected, err := agentshare.MessageFromStorage(*message)
			if err != nil {
				return failedLoop(err)
			}
			s.messages = append(s.messages, projected)
		}
	}
	if err := s.requestCtx.Err(); err != nil {
		return agentshare.LoopResult{Outcome: agentshare.Canceled, Err: err, QuietCancellation: true}
	}

	return agentshare.LoopResult{
		Outcome: agentshare.Completed,
		Commit: agentshare.ReplyCommitInput{
			Persistence:  r.Messages.Committer("append_assistant_message"),
			Session:      s.session,
			Text:         finalText,
			RawText:      finalRawText,
			PlatformText: platformFinalText,
			Stream:       finalStream,
			Outputs:      deferredOutputs,
		},
	}
}

func failedLoop(err error) agentshare.LoopResult {
	return agentshare.LoopResult{Outcome: agentshare.Failed, Err: err}
}
