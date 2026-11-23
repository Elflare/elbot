package chat

import (
	"context"
	"strings"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/delivery"
	"elbot/internal/operationresult"
	"elbot/internal/request"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/storage"
)

func (p *preparedLoop) RunLoop(ctx, requestCtx context.Context, in agentshare.LoopInput, out agentshare.Output) (result operationresult.Result[agentshare.LoopResult], runErr error) {
	r, s := p.route, p.state
	for i := range p.materials.Loaded.Messages {
		head := &p.materials.Loaded.Messages[i]
		if head.ToolBatchStatus != "pending" {
			continue
		}
		head.ToolBatchStatus = "interrupted"
		if err := r.Messages.Commit(s.RequestCtx, storage.DialogueCommit{SessionID: s.Session.ID, ToolResult: &storage.ToolResultCommit{Call: head}}, "close_interrupted_tools"); err != nil {
			return failedLoop(err)
		}
	}
	defer func() {
		if result.Status == operationresult.Success {
			result.Data.Usage, result.Data.Selection = s.usage, s.Selection
		}
	}()
	summarizing := false
	for {
		if !summarizing {
			checked, err := s.RefreshSession(r.Shared, r.CheckSelection, func() error {
				prompt, err := r.PromptBuilder.Build(s.RequestCtx, PromptBuildRequest{Session: s.Session, Scope: r.Identity.Scope(s.RequestCtx)})
				if err != nil {
					return err
				}
				s.messages = agentshare.ReplaceSystemMessages(s.messages, prompt)
				s.tools, err = r.Shared.Tools.Schemas(s.RequestCtx, s.Session)
				return err
			})
			if err != nil {
				return failedLoop(err)
			}
			if checked.Status != operationresult.Success {
				return operationresult.Result[agentshare.LoopResult]{Status: checked.Status, Message: checked.Message}, nil
			}
		}
		pending, stream := s.StartCall(r.Shared, out)
		var snapshot runtimestatus.Snapshot
		if !summarizing {
			snapshot = s.LLMSnapshot(s.usage)
			snapshot.Kind, snapshot.Label = request.KindTurn, "chat"
			r.Status.Record(s.Ctx, snapshot)
		}
		called, err := r.Caller.Call(s.RequestCtx, s.Session, s.Selection, s.messages, s.tools, pending, stream)
		if err != nil {
			return failedLoop(err)
		}
		if called.Status != operationresult.Success {
			return operationresult.Result[agentshare.LoopResult]{Status: called.Status, Message: called.Message}, nil
		}
		reply := called.Data
		if reply.Usage != nil {
			s.usage = reply.Usage
		}
		if !summarizing {
			if len(reply.Messages) > 0 {
				s.messages = reply.Messages
			}
			if err := s.RequestCtx.Err(); err != nil {
				return failedLoop(err)
			}
			snapshot.Usage = s.usage
			r.Status.Record(s.Ctx, snapshot)
		} else {
			if len(reply.ToolCalls) > 0 {
				// TODO: force tool_choice=none when supported; ignore calls in this tool-free summary.
				out.SendPreview(s.Ctx, "总结中的工具调用已忽略")
			}
			if reply.Text == "" {
				reply.Text = "工具调用轮次已达到上限，当前流程已停止。"
				if reply.RawText == "" {
					reply.RawText = reply.Text
				}
			}
		}
		immediate, later := delivery.SplitByDeliveryTiming(reply.Outputs)
		if _, err := out.Send(s.Ctx, delivery.StreamEvent{Type: delivery.StreamSendExtras, Outputs: immediate}); err != nil {
			return failedLoop(err)
		}
		if summarizing || len(reply.ToolCalls) == 0 {
			if err := s.RequestCtx.Err(); err != nil {
				return failedLoop(err)
			}
			raw := strings.TrimSpace(reply.RawText)
			return operationresult.Result[agentshare.LoopResult]{Status: operationresult.Success, Data: agentshare.LoopResult{
				Outcome: agentshare.Completed,
				Commit: agentshare.ReplyCommitInput{
					Persistence: r.Messages.Committer("append_assistant_message"), Session: s.Session,
					Text: raw, RawText: raw, PlatformText: strings.TrimSpace(reply.Text),
					ReasoningContent: reply.ReasoningContent, Stream: stream, Outputs: later,
				},
			}}, nil
		}
		if err := s.SendToolOutput(out, stream, reply.Text, later); err != nil {
			return failedLoop(err)
		}
		committer := r.NewToolTranscriptCommitter()
		execution, limit := s.RunTools(r.Shared, out, reply.ToolCalls, reply.RawText, reply.ReasoningContent, committer, true, s.skippedToolText)
		if execution.Err != nil {
			return failedLoop(execution.Err)
		}
		if execution.Stopped {
			return operationresult.Result[agentshare.LoopResult]{Status: operationresult.Success, Data: agentshare.LoopResult{Outcome: agentshare.Stopped}}, nil
		}
		s.messages = append(s.messages, committer.Committed()...)
		if limit {
			if err := s.AppendUserText(r.Shared, r.Messages.Committer("append_summary_input"), &s.messages, "工具调用次数已达上限，可请求继续或者总结当前进度。"); err != nil {
				return failedLoop(err)
			}
			s.tools = nil
			summarizing = true
			continue
		}
		s.tools, err = r.Shared.Tools.Schemas(s.Ctx, s.Session)
		if err != nil {
			return failedLoop(err)
		}
		if execution.ConfirmationExtra != "" {
			if err := s.AppendUserText(r.Shared, r.Messages.Committer("append_confirmation_input"), &s.messages, "补充："+execution.ConfirmationExtra); err != nil {
				return failedLoop(err)
			}
		}
	}
}

func failedLoop(err error) (operationresult.Result[agentshare.LoopResult], error) {
	return operationresult.Result[agentshare.LoopResult]{}, err
}
