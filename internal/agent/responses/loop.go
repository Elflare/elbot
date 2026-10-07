package responses

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"elbot/internal/agent/dialogue"
	"elbot/internal/delivery"
	"elbot/internal/hook"
	"elbot/internal/llm"
	api "elbot/internal/llm/responses"
	"elbot/internal/modelmgr"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

type Loop struct {
	Logger     *slog.Logger
	Repository storage.DialogueRepository
	Context    *Context
	Models     *modelmgr.Service
	Turns      *turn.Manager
	View       dialogue.ExecutionView
	Preparer   *dialogue.Preparer
	Tools      *dialogue.ToolExecutor
	Messages   *dialogue.MessageStore
	Calls      *dialogue.CallProcessor
	System     dialogue.SystemPromptManager
}

type preparedLoop struct {
	state     *turnState
	materials dialogue.TurnMaterials
}
type turnState struct {
	route           *Loop
	session         *storage.Session
	ctx, requestCtx context.Context
	selection       modelmgr.Selection
	output          dialogue.Output
	projection      []llm.LLMMessage
	tools           []llm.ToolSchema
	definitions     toolDefinitions
	checkpoint      *storage.NativeCheckpoint
	seed            *storage.NativeSeed
	exchange        *storage.NativeExchange
	response        *api.Response
	sourceText      string
	consumed        []string
	calls           []llm.ToolCallRequest
}

func (r *Loop) PrepareTurn(ctx context.Context, materials dialogue.TurnMaterials) (dialogue.PreparedLoop, error) {
	checkpoint, err := r.Repository.CurrentCheckpoint(ctx, materials.Session.ID)
	if err != nil {
		return nil, err
	}
	seed, err := r.Repository.Seed(ctx, materials.Session.ID)
	if err != nil {
		return nil, err
	}
	if checkpoint == nil && seed == nil {
		if err := r.validateInitialInputs(ctx, materials.Session, materials.Loaded.Messages); err != nil {
			return nil, err
		}
		if len(materials.Loaded.Messages) > 0 {
			if err := checkInputFormat(materials.Session); err != nil {
				return nil, err
			}
		}
	}
	if checkpoint == nil && seed != nil && seed.Consumed {
		return nil, fmt.Errorf("原生 seed 已消费但缺少 checkpoint")
	}
	if seed != nil {
		origin, known, err := session.Origin(materials.Session)
		if err != nil {
			return nil, err
		}
		if !known || seed.Protocol != string(origin.Protocol) || seed.Provider != origin.Provider {
			return nil, fmt.Errorf("原生 seed 厂商或协议不匹配")
		}
	}
	if checkpoint != nil && ((seed == nil && checkpoint.SeedID != "") || (seed != nil && checkpoint.SeedID != seed.ID)) {
		return nil, fmt.Errorf("原生 checkpoint 的根材料不匹配")
	}
	definitions := toolDefinitions{}
	if checkpoint != nil || seed != nil {
		window, err := r.Context.Load(ctx, materials.Session, checkpoint)
		if err != nil {
			return nil, err
		}
		definitions, err = definitionsIn(window.Items)
		if err != nil {
			return nil, err
		}
	}
	return &preparedLoop{materials: materials, state: &turnState{route: r, session: materials.Session, checkpoint: checkpoint, seed: seed, definitions: definitions}}, nil
}

func (p *preparedLoop) InputCommitter() dialogue.MessageCommitter {
	return inputCommitter{state: p.state}
}

func (p *preparedLoop) PrepareInput(ctx, requestCtx context.Context, in dialogue.LoopInput, out dialogue.Output) (*storage.Message, error) {
	s := p.state
	s.ctx, s.requestCtx, s.selection, s.output = ctx, hook.WithReadOnlyCalls(requestCtx), in.Selection, out
	if err := s.requestCtx.Err(); err != nil {
		return nil, err
	}
	if err := s.ensureInputFormat(s.requestCtx); err != nil {
		return nil, err
	}
	instructions, err := s.instructions(s.requestCtx)
	if err != nil {
		return nil, err
	}
	s.projection = []llm.LLMMessage{{Role: llm.RoleSystem, Segments: llm.TextSegments(instructions)}}
	for _, row := range p.materials.Loaded.Messages {
		s.projection = append(s.projection, businessMessage(row))
	}
	user := p.materials.UserMessage
	s.projection = append(s.projection, llm.LLMMessage{Role: llm.RoleUser, Segments: p.materials.Input.Segments})
	s.tools, err = s.route.Tools.Schemas(s.requestCtx, s.session)
	if err != nil {
		return nil, err
	}
	segments, tools, err := s.route.Preparer.PrepareInput(s.requestCtx, in, user, p.materials.Input.PlatformText, p.materials.Input.Segments, s.projection, s.tools)
	if err != nil {
		return nil, err
	}
	s.tools = tools
	user.Content, user.Segments = llm.SegmentsContentText(segments), dialogue.StoredMessageSegments(segments)
	s.projection[len(s.projection)-1].Segments = segments
	return user, nil
}

func businessMessage(row storage.Message) llm.LLMMessage {
	segments := dialogue.MessageSegmentsFromStorage(row.Segments)
	metadata := dialogue.AssistantMessageMetadata(row.Metadata)
	text := row.Content
	if row.Role == storage.RoleAssistant && metadata.RawText != "" {
		text = metadata.RawText
	}
	if len(segments) == 0 {
		segments = llm.TextSegments(text)
	}
	return llm.LLMMessage{Role: llm.MessageRole(row.Role), Segments: segments, Name: dialogue.ToolNameFromMetadata(row.Metadata), ToolCallID: row.ToolCallID, ToolCalls: metadata.ToolCalls}
}

func (s *turnState) instructions(ctx context.Context) (string, error) {
	text, err := s.route.System.Build(ctx, dialogue.SystemPromptRequest{Mode: s.session.Mode, Session: s.session, Scope: s.route.Preparer.Identity.Scope(ctx)})
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("system prompt is required")
	}
	return text, nil
}

func (p *preparedLoop) RunLoop(ctx, requestCtx context.Context, in dialogue.LoopInput, out dialogue.Output) dialogue.LoopResult {
	s := p.state
	s.ctx, s.requestCtx = ctx, hook.WithReadOnlyCalls(requestCtx)
	result := dialogue.LoopResult{Selection: in.Selection}
	fail := func(err error) dialogue.LoopResult {
		result.Outcome = dialogue.FailedOutcome(err)
		result.Err = err
		result.QuietCancellation = s.requestCtx.Err() != nil
		return result
	}
	if err := s.closeInterruptedCalls(s.requestCtx); err != nil {
		return fail(err)
	}
	toolPhase, foregroundPrepared := false, false
	rounds := 0
	for {
		var err error
		s.requestCtx, err = s.route.View.RefreshSession(s.requestCtx, s.session)
		if err != nil {
			return fail(err)
		}
		s.ctx = s.route.View.Context(s.ctx)
		if session.WasPromoted(s.session) && !foregroundPrepared {
			foregroundPrepared = true
			s.selection = modelmgr.SelectionForTurn(s.requestCtx, s.route.Models, s.session)
			s.requestCtx, err = s.route.View.WithModel(s.requestCtx, s.selection)
			if err == nil {
				s.ctx, err = s.route.View.WithModel(s.ctx, s.selection)
			}
			if err != nil {
				return fail(err)
			}
			s.tools, err = s.route.Tools.Schemas(s.requestCtx, s.session)
			if err != nil {
				return fail(err)
			}
			instructions, err := s.instructions(s.requestCtx)
			if err != nil {
				return fail(err)
			}
			updated := []llm.LLMMessage{{Role: llm.RoleSystem, Segments: llm.TextSegments(instructions)}}
			for _, message := range s.projection {
				if message.Role != llm.RoleSystem {
					updated = append(updated, message)
				}
			}
			s.projection = updated
		}
		result.Selection = s.selection
		if err := s.route.View.CheckSelection(s.session, s.selection); err != nil {
			return fail(err)
		}
		var pending *dialogue.PendingUserMessage
		if toolPhase {
			pending = dialogue.DrainPending(s.route.Turns, s.session.ID, turn.AttemptFromContext(s.ctx))
			if pending != nil {
				pending.MessageIndex = len(s.projection)
				s.projection = append(s.projection, llm.LLMMessage{Role: llm.RoleUser, Segments: pending.Segments})
			}
		}
		stream := out.StartStream(s.requestCtx)
		out.PublishRuntimeStatus(s.ctx, runtimestatus.Snapshot{SessionID: s.session.ID, Phase: runtimestatus.PhaseLLM, Provider: s.selection.Provider, Model: s.selection.Model, Mode: s.session.Mode, RequestID: in.RequestID, TurnStartedAt: in.StartedAt, StageStartedAt: storage.Now(), Usage: result.Usage})
		final, err := s.call(s.requestCtx, pending, stream)
		if err != nil {
			return fail(err)
		}
		result.Usage = final.Usage
		immediate, later := delivery.SplitByDeliveryTiming(final.Outputs)
		if len(s.calls) == 0 {
			if err := out.SendOutputs(s.ctx, immediate); err != nil {
				return fail(err)
			}
			result.Outcome = dialogue.Completed
			result.Commit = dialogue.ReplyCommitInput{Persistence: replyCommitter{state: s}, Session: s.session, Text: s.sourceText, RawText: s.sourceText, PlatformText: final.Text, Stream: stream, Outputs: later}
			return result
		}
		if err := out.FinishIntermediate(s.ctx, s.requestCtx, stream, final.Text, stream != nil); err != nil {
			return fail(err)
		}
		if err := out.SendOutputs(s.ctx, append(immediate, later...)); err != nil {
			return fail(err)
		}
		committer := &toolCommitter{state: s}
		if rounds >= s.route.Tools.MaxRoundsPerTurn() {
			head := dialogue.ToolCallStorageMessage(s.session.ID, s.sourceText, s.sourceText, s.calls)
			if err := committer.Begin(s.requestCtx, &head); err != nil {
				return fail(err)
			}
			for i, call := range s.calls {
				message := llm.LLMMessage{Role: llm.RoleTool, Name: call.Name, ToolCallID: call.ID, Segments: llm.TextSegments("tool call was not executed: max_rounds_per_turn reached")}
				row := dialogue.ToolResultStorageMessage(s.session.ID, message)
				if err := committer.Result(s.requestCtx, i, call, message, &row); err != nil {
					return fail(err)
				}
			}
			s.tools = nil
			out.SendPreview(s.ctx, fmt.Sprintf("已达到 max_rounds_per_turn=%d，正在请求模型总结当前进度。", s.route.Tools.MaxRoundsPerTurn()))
			summaryInput := &storage.Message{ID: storage.NewID(), SessionID: s.session.ID, Role: storage.RoleUser, Content: "已达到工具调用轮数上限，请根据已有结果总结当前进度，不再调用工具。"}
			if err := (inputCommitter{state: s}).Commit(s.requestCtx, summaryInput); err != nil {
				return fail(err)
			}
			s.projection = append(s.projection, businessMessage(*summaryInput))
			pending = dialogue.DrainPending(s.route.Turns, s.session.ID, turn.AttemptFromContext(s.ctx))
			if pending != nil {
				pending.MessageIndex = len(s.projection)
				s.projection = append(s.projection, llm.LLMMessage{Role: llm.RoleUser, Segments: pending.Segments})
			}
			// The next request retains all calls and their explicit skip outputs.
			// It sends no tools, so another function call is a provider error.
			summaryStream := out.StartStream(s.requestCtx)
			summary, err := s.call(s.requestCtx, pending, summaryStream)
			if err != nil {
				return fail(err)
			}
			if len(s.calls) > 0 {
				return fail(fmt.Errorf("summary response returned function calls with no tools"))
			}
			result.Usage = summary.Usage
			immediate, later = delivery.SplitByDeliveryTiming(summary.Outputs)
			if err := out.SendOutputs(s.ctx, immediate); err != nil {
				return fail(err)
			}
			result.Outcome = dialogue.Completed
			result.Commit = dialogue.ReplyCommitInput{Persistence: replyCommitter{state: s}, Session: s.session, Text: s.sourceText, RawText: s.sourceText, PlatformText: summary.Text, Stream: summaryStream, Outputs: later}
			return result
		}
		if !toolPhase {
			if !s.route.Turns.StartToolPhase(s.session.ID, turn.AttemptFromContext(s.ctx)) {
				result.Outcome = dialogue.Stopped
				return result
			}
			toolPhase = true
		}
		rounds++
		execution := s.route.Tools.ExecuteWithCommitter(s.requestCtx, s.session, s.calls, s.sourceText, s.sourceText, out, committer)
		if execution.Err != nil {
			return fail(execution.Err)
		}
		if execution.Stopped {
			if err := s.requestCtx.Err(); err != nil {
				return fail(err)
			}
			result.Outcome = dialogue.Stopped
			return result
		}
		s.projection = append(s.projection, llm.LLMMessage{Role: llm.RoleAssistant, Segments: llm.TextSegments(s.sourceText), ToolCalls: append([]llm.ToolCallRequest(nil), s.calls...)})
		s.projection = append(s.projection, execution.Messages...)
		if execution.ConfirmationExtra != "" {
			message := &storage.Message{ID: storage.NewID(), SessionID: s.session.ID, Role: storage.RoleUser, Content: "补充：" + execution.ConfirmationExtra}
			if err := (inputCommitter{state: s}).Commit(s.requestCtx, message); err != nil {
				return fail(err)
			}
			s.projection = append(s.projection, businessMessage(*message))
		}
		s.tools, err = s.route.Tools.Schemas(s.requestCtx, s.session)
		if err != nil {
			return fail(err)
		}
	}
}
