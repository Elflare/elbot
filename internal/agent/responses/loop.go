package responses

import (
	"context"
	"fmt"
	"strings"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/contextmgr"
	"elbot/internal/delivery"
	"elbot/internal/llm"
	api "elbot/internal/llm/responses"
	"elbot/internal/modelmgr"
	"elbot/internal/operationresult"
	"elbot/internal/session"
	"elbot/internal/storage"
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
	agentshare.LoopState
	projection  []llm.LLMMessage
	tools       []llm.ToolSchema
	definitions toolDefinitions
	checkpoint  *storage.NativeCheckpoint
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
	return operationresult.Result[agentshare.PreparedLoop]{Status: operationresult.Success, Data: &preparedLoop{materials: materials, state: &turnState{route: r, LoopState: agentshare.LoopState{LoopInput: agentshare.LoopInput{Session: materials.Session}}, checkpoint: checkpoint, seed: seed, window: window, definitions: definitions}}}, nil
}

func (p *preparedLoop) InputCommitter() agentshare.MessageCommitter {
	return inputCommitter{state: p.state}
}

func (p *preparedLoop) PrepareInput(ctx, requestCtx context.Context, in agentshare.LoopInput, out agentshare.Output) (operationresult.Result[storage.DialogueCommit], error) {
	s := p.state
	s.Ctx, s.RequestCtx, s.LoopInput = ctx, agentshare.WithReadOnlyCalls(requestCtx), in
	if err := s.RequestCtx.Err(); err != nil {
		return operationresult.Result[storage.DialogueCommit]{}, err
	}
	checked, err := s.ensureInputFormat(s.RequestCtx)
	if err != nil {
		return operationresult.Result[storage.DialogueCommit]{}, err
	}
	if checked.Status != operationresult.Success {
		return operationresult.Result[storage.DialogueCommit]{Status: checked.Status, Message: checked.Message}, nil
	}
	instructions, err := s.instructions(s.RequestCtx)
	if err != nil {
		return operationresult.Result[storage.DialogueCommit]{}, err
	}
	s.projection = []llm.LLMMessage{{Role: llm.RoleSystem, Segments: llm.TextSegments(instructions)}}
	for _, row := range p.materials.Loaded.Messages {
		projected, err := agentshare.MessageFromStorage(row)
		if err != nil {
			return operationresult.Result[storage.DialogueCommit]{}, err
		}
		s.projection = append(s.projection, projected)
	}
	user := p.materials.UserMessage
	for _, m := range p.materials.Input.Reports {
		projected, err := agentshare.MessageFromStorage(*m)
		if err != nil {
			return operationresult.Result[storage.DialogueCommit]{}, err
		}
		s.projection = append(s.projection, projected)
	}
	if user != nil {
		s.projection = append(s.projection, llm.LLMMessage{Role: llm.RoleUser, Segments: p.materials.Input.Segments})
	}
	s.tools, err = s.route.Shared.Tools.Schemas(s.RequestCtx, s.Session)
	if err != nil {
		return operationresult.Result[storage.DialogueCommit]{}, err
	}
	if user == nil {
		return operationresult.Result[storage.DialogueCommit]{Status: operationresult.Success, Data: p.materials.InputCommit(nil, false)}, nil
	}
	segments, tools, elyphRulesShown, err := s.route.Preparer.PrepareInput(s.RequestCtx, in, user, p.materials.Input.PlatformText, p.materials.Input.Segments, s.projection, s.tools, p.materials.Input.NeedsElyphRules)
	if err != nil {
		return operationresult.Result[storage.DialogueCommit]{}, err
	}
	s.tools = tools
	s.projection[len(s.projection)-1].Segments = segments
	return operationresult.Result[storage.DialogueCommit]{Status: operationresult.Success, Data: p.materials.InputCommit(segments, elyphRulesShown)}, nil
}

func (s *turnState) instructions(ctx context.Context) (string, error) {
	text, err := s.route.System.Build(ctx, agentshare.SystemPromptRequest{Mode: s.Session.Mode, Session: s.Session, Scope: s.route.Identity.Scope(ctx)})
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
	s.Ctx, s.RequestCtx = ctx, agentshare.WithReadOnlyCalls(requestCtx)
	result := agentshare.LoopResult{Selection: in.Selection}
	fail := func(err error) (operationresult.Result[agentshare.LoopResult], error) {
		return operationresult.Result[agentshare.LoopResult]{}, err
	}
	if err := s.closeInterruptedCalls(s.RequestCtx, p.materials.Loaded.Messages); err != nil {
		return fail(err)
	}
	d := s.route.Shared
	summarizing := false
	for {
		if !summarizing {
			compatible, err := s.RefreshSession(d, s.route.CheckSelection, func() error {
				var err error
				s.tools, err = d.Tools.Schemas(s.RequestCtx, s.Session)
				if err != nil {
					return err
				}
				instructions, err := s.instructions(s.RequestCtx)
				if err != nil {
					return err
				}
				s.projection = agentshare.ReplaceSystemMessages(s.projection, []llm.LLMMessage{{Role: llm.RoleSystem, Segments: llm.TextSegments(instructions)}})
				return nil
			})
			if err != nil {
				return fail(err)
			}
			if compatible.Status != operationresult.Success {
				return operationresult.Result[agentshare.LoopResult]{Status: compatible.Status, Message: compatible.Message}, nil
			}
			result.Selection = s.Selection
		}
		pending, stream := s.StartCall(d, out)
		if !summarizing {
			s.route.Status.Record(s.Ctx, s.LLMSnapshot(result.Usage))
		}
		called, err := s.call(s.RequestCtx, pending, stream)
		if err != nil {
			return fail(err)
		}
		if called.Status != operationresult.Success {
			return operationresult.Result[agentshare.LoopResult]{Status: called.Status, Message: called.Message}, nil
		}
		if summarizing && len(s.calls) != 0 {
			return fail(fmt.Errorf("summary response returned function calls with no tools"))
		}
		final := called.Data
		result.Usage = final.Usage
		immediate, later := delivery.SplitByDeliveryTiming(final.Outputs)
		if len(s.calls) == 0 {
			if _, err := out.Send(s.Ctx, delivery.StreamEvent{Type: delivery.StreamSendExtras, Outputs: immediate}); err != nil {
				return fail(err)
			}
			result.Outcome = agentshare.Completed
			result.Commit = agentshare.ReplyCommitInput{
				Persistence:      replyCommitter{state: s},
				Session:          s.Session,
				Text:             s.sourceText,
				RawText:          s.sourceText,
				ReasoningContent: final.ReasoningContent,
				PlatformText:     final.Text,
				Stream:           stream,
				Outputs:          later,
			}
			return operationresult.Result[agentshare.LoopResult]{Status: operationresult.Success, Data: result}, nil
		}
		if err := s.SendToolOutput(out, stream, final.Text, append(immediate, later...)); err != nil {
			return fail(err)
		}
		execution, limit := s.RunTools(d, out, s.calls, s.sourceText, final.ReasoningContent, &toolCommitter{state: s}, false, func(int) string {
			return "tool call was not executed: max_rounds_per_turn reached"
		})
		if execution.Err != nil {
			return fail(execution.Err)
		}
		if execution.Stopped {
			result.Outcome = agentshare.Stopped
			return operationresult.Result[agentshare.LoopResult]{Status: operationresult.Success, Data: result}, nil
		}
		if limit {
			s.tools = nil
			out.SendPreview(s.Ctx, fmt.Sprintf("max_rounds_per_turn=%d → 总结", d.Tools.MaxRoundsPerTurn()))
			if err := s.AppendUserText(d, inputCommitter{state: s}, &s.projection, "已达到工具调用轮数上限，请根据已有结果总结当前进度，不再调用工具。"); err != nil {
				return fail(err)
			}
			summarizing = true
			continue
		}
		if execution.ConfirmationExtra != "" {
			if err := s.AppendUserText(d, inputCommitter{state: s}, &s.projection, "补充："+execution.ConfirmationExtra); err != nil {
				return fail(err)
			}
		}
		s.tools, err = d.Tools.Schemas(s.RequestCtx, s.Session)
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
		compactor: &Compactor{Context: nativeContext, Gate: deps.Gate, View: deps.Shared.View, System: deps.System, Identity: deps.Identity},
	}
}

func (r *Loop) Compactor() contextmgr.Compactor    { return r.compactor }
func (r *Loop) Material() session.MaterialPreparer { return r.Context }
