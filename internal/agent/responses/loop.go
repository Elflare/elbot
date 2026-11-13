package responses

import (
	"context"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/operationresult"
	"fmt"
	"strings"

	"elbot/internal/contextmgr"
	"elbot/internal/delivery"
	"elbot/internal/llm"
	api "elbot/internal/llm/responses"
	"elbot/internal/modelmgr"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

type Loop struct {
	LoopDeps
	Context   *Context
	compactor contextmgr.Compactor
}

type preparedLoop struct {
	state     *turnState
	materials agentshare.TurnMaterials
}

// CheckSelection validates the native capability before any turn input is saved.
func (r *Loop) CheckSelection(selection modelmgr.Selection) operationresult.Result[struct{}] {
	if _, ok := selection.Client.(api.Streamer); !ok {
		return operationresult.Result[struct{}]{Status: operationresult.Incompatible, Message: fmt.Sprintf("provider %q does not provide Responses streaming", selection.Provider)}
	}
	return operationresult.Result[struct{}]{Status: operationresult.Success}
}

type turnState struct {
	requestReferences []inputReference
	route             *Loop
	session           *storage.Session
	ctx, requestCtx   context.Context
	selection         modelmgr.Selection
	output            agentshare.Output
	projection        []llm.LLMMessage
	tools             []llm.ToolSchema
	definitions       toolDefinitions
	checkpoint        *storage.NativeCheckpoint
	// window retains unresolved material for the current immutable checkpoint.
	window     *nativeWindow
	seed       *storage.NativeSeed
	exchange   *storage.NativeExchange
	response   *api.Response
	sourceText string
	consumed   []string
	calls      []llm.ToolCallRequest
}

func (r *Loop) PrepareTurn(ctx context.Context, materials agentshare.TurnMaterials) (operationresult.Result[agentshare.PreparedLoop], error) {
	checkpoint, err := r.Repository.CurrentCheckpoint(ctx, materials.Session.ID)
	if err != nil {
		return operationresult.Result[agentshare.PreparedLoop]{}, err
	}
	records, err := r.Repository.LoadNativeWindow(ctx, materials.Session, checkpoint)
	if err != nil {
		return operationresult.Result[agentshare.PreparedLoop]{}, err
	}
	seed := records.Seed
	if checkpoint == nil && seed == nil {
		if len(materials.Loaded.Messages) > 0 {
			checked, err := checkInputFormat(materials.Session)
			if err != nil {
				return operationresult.Result[agentshare.PreparedLoop]{}, err
			}
			if checked.Status != operationresult.Success {
				return operationresult.Result[agentshare.PreparedLoop]{Status: checked.Status, Message: checked.Message}, nil
			}
		}
		if err := r.validateInitialInputs(ctx, materials.Session, materials.Loaded.Messages); err != nil {
			return operationresult.Result[agentshare.PreparedLoop]{}, err
		}
	}
	if checkpoint == nil && seed != nil && seed.Consumed {
		return operationresult.Result[agentshare.PreparedLoop]{}, fmt.Errorf("原生 seed 已消费但缺少 checkpoint")
	}
	if seed != nil {
		origin, known, err := session.Origin(materials.Session)
		if err != nil {
			return operationresult.Result[agentshare.PreparedLoop]{}, err
		}
		if !known || seed.APIType != string(origin.APIType) || seed.Provider != origin.Provider {
			return operationresult.Result[agentshare.PreparedLoop]{}, fmt.Errorf("原生 seed 厂商或协议不匹配")
		}
	}
	if checkpoint != nil && ((seed == nil && checkpoint.SeedID != "") || (seed != nil && checkpoint.SeedID != seed.ID)) {
		return operationresult.Result[agentshare.PreparedLoop]{}, fmt.Errorf("原生 checkpoint 的根材料不匹配")
	}
	definitions := toolDefinitions{}
	var window *nativeWindow
	if checkpoint != nil || seed != nil {
		loaded, err := r.Context.Load(ctx, materials.Session, checkpoint, records)
		if err != nil {
			return operationresult.Result[agentshare.PreparedLoop]{}, err
		}
		if loaded.Status != operationresult.Success {
			return operationresult.Result[agentshare.PreparedLoop]{Status: loaded.Status, Message: loaded.Message}, nil
		}
		window = loaded.Data
		definitions, err = definitionsIn(window.Items)
		if err != nil {
			return operationresult.Result[agentshare.PreparedLoop]{}, err
		}
	}
	return operationresult.Result[agentshare.PreparedLoop]{Status: operationresult.Success, Data: &preparedLoop{materials: materials, state: &turnState{route: r, session: materials.Session, checkpoint: checkpoint, seed: seed, window: window, definitions: definitions}}}, nil
}

func (p *preparedLoop) InputCommitter() agentshare.MessageCommitter {
	return inputCommitter{state: p.state}
}

func (p *preparedLoop) PrepareInput(ctx, requestCtx context.Context, in agentshare.LoopInput, out agentshare.Output) (operationresult.Result[*storage.Message], error) {
	s := p.state
	s.ctx, s.requestCtx, s.selection, s.output = ctx, agentshare.WithReadOnlyCalls(requestCtx), in.Selection, out
	if err := s.requestCtx.Err(); err != nil {
		return operationresult.Result[*storage.Message]{}, err
	}
	checked, err := s.ensureInputFormat(s.requestCtx)
	if err != nil {
		return operationresult.Result[*storage.Message]{}, err
	}
	if checked.Status != operationresult.Success {
		return operationresult.Result[*storage.Message]{Status: checked.Status, Message: checked.Message}, nil
	}
	instructions, err := s.instructions(s.requestCtx)
	if err != nil {
		return operationresult.Result[*storage.Message]{}, err
	}
	s.projection = []llm.LLMMessage{{Role: llm.RoleSystem, Segments: llm.TextSegments(instructions)}}
	for _, row := range p.materials.Loaded.Messages {
		projected, err := agentshare.MessageFromStorage(row)
		if err != nil {
			return operationresult.Result[*storage.Message]{}, err
		}
		s.projection = append(s.projection, projected)
	}
	user := p.materials.UserMessage
	s.projection = append(s.projection, llm.LLMMessage{Role: llm.RoleUser, Segments: p.materials.Input.Segments})
	s.tools, err = s.route.Tools.Schemas(s.requestCtx, s.session)
	if err != nil {
		return operationresult.Result[*storage.Message]{}, err
	}
	segments, tools, err := s.route.Preparer.PrepareInput(s.requestCtx, in, user, p.materials.Input.PlatformText, p.materials.Input.Segments, s.projection, s.tools, p.materials.Input.NeedsElyphRules)
	if err != nil {
		return operationresult.Result[*storage.Message]{}, err
	}
	s.tools = tools
	user.Content, user.Segments = agentshare.MessageContent(segments), agentshare.StoredMessageSegments(segments)
	s.projection[len(s.projection)-1].Segments = segments
	return operationresult.Result[*storage.Message]{Status: operationresult.Success, Data: user}, nil
}

func (s *turnState) instructions(ctx context.Context) (string, error) {
	text, err := s.route.System.Build(ctx, agentshare.SystemPromptRequest{Mode: s.session.Mode, Session: s.session, Scope: s.route.Identity.Scope(ctx)})
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("system prompt is required")
	}
	return text, nil
}

func (p *preparedLoop) RunLoop(ctx, requestCtx context.Context, in agentshare.LoopInput, out agentshare.Output) (operationresult.Result[agentshare.LoopResult], error) {
	s := p.state
	s.ctx, s.requestCtx = ctx, agentshare.WithReadOnlyCalls(requestCtx)
	result := agentshare.LoopResult{Selection: in.Selection}
	fail := func(err error) (operationresult.Result[agentshare.LoopResult], error) {
		return operationresult.Result[agentshare.LoopResult]{}, err
	}
	if err := s.closeInterruptedCalls(s.requestCtx, p.materials.Loaded.Messages); err != nil {
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
			s.selection = s.route.SelectionForTurn(s.requestCtx, s.session)
			if checked := s.route.CheckSelection(s.selection); checked.Status != operationresult.Success {
				return operationresult.Result[agentshare.LoopResult]{Status: checked.Status, Message: checked.Message}, nil
			}
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
		compatible, err := s.route.View.CheckSelection(s.session, s.selection)
		if err != nil {
			return fail(err)
		}
		if compatible.Status != operationresult.Success {
			return operationresult.Result[agentshare.LoopResult]{Status: compatible.Status, Message: compatible.Message}, nil
		}
		var pending *agentshare.PendingUserMessage
		if toolPhase {
			pending = agentshare.DrainPending(s.route.Turns, s.session.ID, turn.AttemptFromContext(s.ctx))
			if pending != nil {
				pending.MessageIndex = len(s.projection)
				s.projection = append(s.projection, llm.LLMMessage{Role: llm.RoleUser, Segments: pending.Segments})
			}
		}
		stream := out.StartStream(s.requestCtx)
		s.route.Status.Record(s.ctx, runtimestatus.Snapshot{
			SessionID:      s.session.ID,
			Phase:          runtimestatus.PhaseLLM,
			Provider:       s.selection.Provider,
			Model:          s.selection.Model,
			Mode:           s.session.Mode,
			RequestID:      in.RequestID,
			TurnStartedAt:  in.StartedAt,
			StageStartedAt: storage.Now(),
			Usage:          result.Usage,
		})
		called, err := s.call(s.requestCtx, pending, stream)
		if err != nil {
			return fail(err)
		}
		if called.Status != operationresult.Success {
			return operationresult.Result[agentshare.LoopResult]{Status: called.Status, Message: called.Message}, nil
		}
		final := called.Data
		result.Usage = final.Usage
		immediate, later := delivery.SplitByDeliveryTiming(final.Outputs)
		if len(s.calls) == 0 {
			if _, err := out.Send(s.ctx, delivery.StreamEvent{Type: delivery.StreamSendExtras, Outputs: immediate}); err != nil {
				return fail(err)
			}
			result.Outcome = agentshare.Completed
			result.Commit = agentshare.ReplyCommitInput{
				Persistence:      replyCommitter{state: s},
				Session:          s.session,
				Text:             s.sourceText,
				RawText:          s.sourceText,
				ReasoningContent: final.ReasoningContent,
				PlatformText:     final.Text,
				Stream:           stream,
				Outputs:          later,
			}
			return operationresult.Result[agentshare.LoopResult]{Status: operationresult.Success, Data: result}, nil
		}
		if _, err := stream.Send(s.requestCtx, delivery.StreamEvent{Type: delivery.StreamSend, Text: final.Text}); err != nil {
			return fail(err)
		}
		if _, err := out.Send(s.ctx, delivery.StreamEvent{Type: delivery.StreamSendExtras, Outputs: append(immediate, later...)}); err != nil {
			return fail(err)
		}
		committer := &toolCommitter{state: s}
		if rounds >= s.route.Tools.MaxRoundsPerTurn() {
			head := agentshare.ToolCallStorageMessage(s.session.ID, s.sourceText, s.sourceText, s.calls, final.ReasoningContent)
			if err := committer.Begin(s.requestCtx, &head); err != nil {
				return fail(err)
			}
			for i, call := range s.calls {
				message := llm.LLMMessage{Role: llm.RoleTool, Name: call.Name, ToolCallID: call.ID, Segments: llm.TextSegments("tool call was not executed: max_rounds_per_turn reached")}
				row := agentshare.ToolResultStorageMessage(s.session.ID, message)
				if err := committer.Result(s.requestCtx, i, call, message, &storage.ToolResultCommit{Result: &row}); err != nil {
					return fail(err)
				}
			}
			if err := committer.Flush(s.requestCtx); err != nil {
				return fail(err)
			}
			s.tools = nil
			out.SendPreview(s.ctx, fmt.Sprintf("max_rounds_per_turn=%d → 总结", s.route.Tools.MaxRoundsPerTurn()))
			summaryInput := &storage.Message{
				SessionID: s.session.ID,
				Role:      storage.RoleUser,
				Content:   "已达到工具调用轮数上限，请根据已有结果总结当前进度，不再调用工具。",
			}
			if err := storage.PrepareMessageID(s.requestCtx, s.route.NewMessageID, summaryInput); err != nil {
				return fail(err)
			}
			if err := (inputCommitter{state: s}).Commit(s.requestCtx, summaryInput, nil); err != nil {
				return fail(err)
			}
			projected, err := agentshare.MessageFromStorage(*summaryInput)
			if err != nil {
				return fail(err)
			}
			s.projection = append(s.projection, projected)
			pending = agentshare.DrainPending(s.route.Turns, s.session.ID, turn.AttemptFromContext(s.ctx))
			if pending != nil {
				pending.MessageIndex = len(s.projection)
				s.projection = append(s.projection, llm.LLMMessage{Role: llm.RoleUser, Segments: pending.Segments})
			}
			// The next request retains all calls and their explicit skip outputs.
			// It sends no tools, so another function call is a provider error.
			stream = out.StartStream(s.requestCtx)
			summarized, err := s.call(s.requestCtx, pending, stream)
			if err != nil {
				return fail(err)
			}
			if summarized.Status != operationresult.Success {
				return operationresult.Result[agentshare.LoopResult]{Status: summarized.Status, Message: summarized.Message}, nil
			}
			summary := summarized.Data
			if len(s.calls) > 0 {
				return fail(fmt.Errorf("summary response returned function calls with no tools"))
			}
			result.Usage = summary.Usage
			immediate, later = delivery.SplitByDeliveryTiming(summary.Outputs)
			if _, err := out.Send(s.ctx, delivery.StreamEvent{Type: delivery.StreamSendExtras, Outputs: immediate}); err != nil {
				return fail(err)
			}
			result.Outcome = agentshare.Completed
			result.Commit = agentshare.ReplyCommitInput{
				Persistence:      replyCommitter{state: s},
				Session:          s.session,
				Text:             s.sourceText,
				RawText:          s.sourceText,
				ReasoningContent: summary.ReasoningContent,
				PlatformText:     summary.Text,
				Stream:           stream,
				Outputs:          later,
			}
			return operationresult.Result[agentshare.LoopResult]{Status: operationresult.Success, Data: result}, nil
		}
		if !toolPhase {
			if !s.route.Turns.StartToolPhase(s.session.ID, turn.AttemptFromContext(s.ctx)) {
				result.Outcome = agentshare.Stopped
				return operationresult.Result[agentshare.LoopResult]{Status: operationresult.Success, Data: result}, nil
			}
			toolPhase = true
		}
		rounds++
		execution := s.route.Tools.ExecuteWithCommitter(s.requestCtx, s.session, s.calls, s.sourceText, s.sourceText, final.ReasoningContent, out, committer)
		if execution.Err != nil {
			return fail(execution.Err)
		}
		if execution.Stopped {
			if err := s.requestCtx.Err(); err != nil {
				return fail(err)
			}
			result.Outcome = agentshare.Stopped
			return operationresult.Result[agentshare.LoopResult]{Status: operationresult.Success, Data: result}, nil
		}
		if execution.ConfirmationExtra != "" {
			message := &storage.Message{SessionID: s.session.ID, Role: storage.RoleUser, Content: "补充：" + execution.ConfirmationExtra}
			if err := storage.PrepareMessageID(s.requestCtx, s.route.NewMessageID, message); err != nil {
				return fail(err)
			}
			if err := (inputCommitter{state: s}).Commit(s.requestCtx, message, nil); err != nil {
				return fail(err)
			}
			projected, err := agentshare.MessageFromStorage(*message)
			if err != nil {
				return fail(err)
			}
			s.projection = append(s.projection, projected)
		}
		s.tools, err = s.route.Tools.Schemas(s.requestCtx, s.session)
		if err != nil {
			return fail(err)
		}
	}
}

func NewLoop(deps LoopDeps) *Loop {
	nativeContext := &Context{Repository: deps.Repository, Preparer: deps.Preparer}
	return &Loop{
		LoopDeps:  deps,
		Context:   nativeContext,
		compactor: &Compactor{Context: nativeContext, Gate: deps.Gate, View: deps.View, System: deps.System, Identity: deps.Identity},
	}
}

func (r *Loop) Compactor() contextmgr.Compactor    { return r.compactor }
func (r *Loop) Material() session.MaterialPreparer { return r.Context }
