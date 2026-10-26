package responses

import (
	"context"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/events"
	"elbot/internal/tool"
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
	Identity interface {
		Scope(context.Context) session.Scope
	}
	SelectionForTurn func(context.Context, *storage.Session) modelmgr.Selection
	Gate             interface {
		Enter(context.Context, string) (context.Context, func(), error)
	}
	SessionRows interface {
		Mutate(context.Context, string, func(*storage.Session) error) (*storage.Session, error)
	}

	Status interface {
		Record(context.Context, runtimestatus.Snapshot)
	}
	compactor  contextmgr.Compactor
	Repository interface {
		CreateExchange(context.Context, *storage.NativeExchange) error
		FinishExchange(context.Context, string, string, string, string, string) error
		CurrentCheckpoint(context.Context, string) (*storage.NativeCheckpoint, error)
		CheckpointForMessage(context.Context, string, string) (*storage.NativeCheckpoint, error)
		LoadNativeWindow(context.Context, *storage.Session, *storage.NativeCheckpoint) (*storage.NativeWindowRecords, error)
		PendingInputs(context.Context, string) ([]storage.NativeInput, error)
		Calls(context.Context, string) ([]storage.NativeCall, error)
	}
	Context *Context
	Turns   interface {
		StartToolPhase(string, ...string) bool
		DrainMergedInput(string, ...string) turn.Input
	}
	View interface {
		Context(context.Context) context.Context
		RefreshSession(context.Context, *storage.Session) (context.Context, error)
		WithModel(context.Context, modelmgr.Selection) (context.Context, error)
		CheckSelection(*storage.Session, modelmgr.Selection) error
	}
	Preparer interface {
		PrepareInput(context.Context, agentshare.LoopInput, *storage.Message, string, []llm.MessageSegment, []llm.LLMMessage, []llm.ToolSchema, bool) ([]llm.MessageSegment, []llm.ToolSchema, error)
		ArchiveMaterials(context.Context, []llm.LLMMessage) ([]llm.LLMMessage, error)
		PrepareMaterials(context.Context, []llm.LLMMessage) ([]llm.LLMMessage, func(), error)
	}
	Tools interface {
		Schemas(context.Context, *storage.Session) ([]llm.ToolSchema, error)
		MaxRoundsPerTurn() int
		ExecuteWithCommitter(context.Context, *storage.Session, []llm.ToolCallRequest, string, string, agentshare.Output, tool.ToolCommitter) tool.RunResult
	}
	Messages interface {
		Commit(context.Context, storage.DialogueCommit, string) (events.LogResult, error)
	}
	Calls interface {
		Begin(string, modelmgr.Selection) agentshare.CallObservation
		PrepareCall(context.Context, *storage.Session, modelmgr.Selection, []llm.LLMMessage, []llm.ToolSchema, *agentshare.PendingUserMessage, agentshare.MessageCommitter, *events.LogRecord) (agentshare.CallInput, error)
		NotifyError(context.Context, string, agentshare.LLMResponse, error)
		CompleteCall(context.Context, *storage.Session, modelmgr.Selection, string, *llm.Usage, []llm.ToolCallRequest, int64) (agentshare.CallOutput, error)
	}
	System interface {
		Build(context.Context, agentshare.SystemPromptRequest) (string, error)
	}
}

type preparedLoop struct {
	state     *turnState
	materials agentshare.TurnMaterials
}

// CheckSelection validates the native capability before any turn input is saved.
func (r *Loop) CheckSelection(selection modelmgr.Selection) error {
	if _, ok := selection.Client.(api.Streamer); !ok {
		return fmt.Errorf("provider %q does not provide Responses streaming", selection.Provider)
	}
	return nil
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

func (r *Loop) PrepareTurn(ctx context.Context, materials agentshare.TurnMaterials) (agentshare.PreparedLoop, error) {
	checkpoint, err := r.Repository.CurrentCheckpoint(ctx, materials.Session.ID)
	if err != nil {
		return nil, err
	}
	records, err := r.Repository.LoadNativeWindow(ctx, materials.Session, checkpoint)
	if err != nil {
		return nil, err
	}
	seed := records.Seed
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
		if !known || seed.APIType != string(origin.APIType) || seed.Provider != origin.Provider {
			return nil, fmt.Errorf("原生 seed 厂商或协议不匹配")
		}
	}
	if checkpoint != nil && ((seed == nil && checkpoint.SeedID != "") || (seed != nil && checkpoint.SeedID != seed.ID)) {
		return nil, fmt.Errorf("原生 checkpoint 的根材料不匹配")
	}
	definitions := toolDefinitions{}
	var window *nativeWindow
	if checkpoint != nil || seed != nil {
		window, err = r.Context.Load(ctx, materials.Session, checkpoint, records)
		if err != nil {
			return nil, err
		}
		definitions, err = definitionsIn(window.Items)
		if err != nil {
			return nil, err
		}
	}
	return &preparedLoop{materials: materials, state: &turnState{route: r, session: materials.Session, checkpoint: checkpoint, seed: seed, window: window, definitions: definitions}}, nil
}

func (p *preparedLoop) InputCommitter() agentshare.MessageCommitter {
	return inputCommitter{state: p.state}
}

func (p *preparedLoop) PrepareInput(ctx, requestCtx context.Context, in agentshare.LoopInput, out agentshare.Output) (*storage.Message, error) {
	s := p.state
	s.ctx, s.requestCtx, s.selection, s.output = ctx, agentshare.WithReadOnlyCalls(requestCtx), in.Selection, out
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
		projected, err := agentshare.MessageFromStorage(row)
		if err != nil {
			return nil, err
		}
		s.projection = append(s.projection, projected)
	}
	user := p.materials.UserMessage
	s.projection = append(s.projection, llm.LLMMessage{Role: llm.RoleUser, Segments: p.materials.Input.Segments})
	s.tools, err = s.route.Tools.Schemas(s.requestCtx, s.session)
	if err != nil {
		return nil, err
	}
	segments, tools, err := s.route.Preparer.PrepareInput(s.requestCtx, in, user, p.materials.Input.PlatformText, p.materials.Input.Segments, s.projection, s.tools, p.materials.Input.NeedsElyphRules)
	if err != nil {
		return nil, err
	}
	s.tools = tools
	user.Content, user.Segments = agentshare.MessageContent(segments), agentshare.StoredMessageSegments(segments)
	s.projection[len(s.projection)-1].Segments = segments
	return user, nil
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

func (p *preparedLoop) RunLoop(ctx, requestCtx context.Context, in agentshare.LoopInput, out agentshare.Output) agentshare.LoopResult {
	s := p.state
	s.ctx, s.requestCtx = ctx, agentshare.WithReadOnlyCalls(requestCtx)
	result := agentshare.LoopResult{Selection: in.Selection}
	fail := func(err error) agentshare.LoopResult {
		result.Outcome = agentshare.Failed
		result.Err = err
		return result
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
			if err := s.route.CheckSelection(s.selection); err != nil {
				return fail(err)
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
		if err := s.route.View.CheckSelection(s.session, s.selection); err != nil {
			return fail(err)
		}
		var pending *agentshare.PendingUserMessage
		if toolPhase {
			pending = agentshare.DrainPending(s.route.Turns, s.session.ID, turn.AttemptFromContext(s.ctx))
			if pending != nil {
				pending.MessageIndex = len(s.projection)
				s.projection = append(s.projection, llm.LLMMessage{Role: llm.RoleUser, Segments: pending.Segments})
			}
		}
		stream, err := out.StartStream(s.requestCtx)
		if err != nil {
			return fail(err)
		}
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
		final, err := s.call(s.requestCtx, pending, stream)
		if err != nil {
			return fail(err)
		}
		result.Usage = final.Usage
		immediate, later := delivery.SplitByDeliveryTiming(final.Outputs)
		if len(s.calls) == 0 {
			if _, err := out.SendOutputs(s.ctx, immediate); err != nil {
				return fail(err)
			}
			result.Outcome = agentshare.Completed
			result.Commit = agentshare.ReplyCommitInput{
				Persistence:  replyCommitter{state: s},
				Session:      s.session,
				Text:         s.sourceText,
				RawText:      s.sourceText,
				PlatformText: final.Text,
				Stream:       stream,
				Outputs:      later,
			}
			return result
		}
		if err := out.FinishIntermediate(s.ctx, s.requestCtx, stream, final.Text, stream != nil); err != nil {
			return fail(err)
		}
		if _, err := out.SendOutputs(s.ctx, append(immediate, later...)); err != nil {
			return fail(err)
		}
		committer := &toolCommitter{state: s}
		if rounds >= s.route.Tools.MaxRoundsPerTurn() {
			head := agentshare.ToolCallStorageMessage(s.session.ID, s.sourceText, s.sourceText, s.calls)
			if _, err := committer.Begin(s.requestCtx, &head); err != nil {
				return fail(err)
			}
			for i, call := range s.calls {
				message := llm.LLMMessage{Role: llm.RoleTool, Name: call.Name, ToolCallID: call.ID, Segments: llm.TextSegments("tool call was not executed: max_rounds_per_turn reached")}
				row := agentshare.ToolResultStorageMessage(s.session.ID, message)
				if _, err := committer.Result(s.requestCtx, i, call, message, &storage.ToolResultCommit{Result: &row}); err != nil {
					return fail(err)
				}
			}
			if _, err := committer.Flush(s.requestCtx); err != nil {
				return fail(err)
			}
			s.tools = nil
			out.SendPreview(s.ctx, fmt.Sprintf("已达到 max_rounds_per_turn=%d，正在请求模型总结当前进度。", s.route.Tools.MaxRoundsPerTurn()))
			summaryInput := &storage.Message{
				ID:        storage.NewID(),
				SessionID: s.session.ID,
				Role:      storage.RoleUser,
				Content:   "已达到工具调用轮数上限，请根据已有结果总结当前进度，不再调用工具。",
			}
			if _, err := (inputCommitter{state: s}).Commit(s.requestCtx, summaryInput, nil); err != nil {
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
			summaryStream, err := out.StartStream(s.requestCtx)
			if err != nil {
				return fail(err)
			}
			summary, err := s.call(s.requestCtx, pending, summaryStream)
			if err != nil {
				return fail(err)
			}
			if len(s.calls) > 0 {
				return fail(fmt.Errorf("summary response returned function calls with no tools"))
			}
			result.Usage = summary.Usage
			immediate, later = delivery.SplitByDeliveryTiming(summary.Outputs)
			if _, err := out.SendOutputs(s.ctx, immediate); err != nil {
				return fail(err)
			}
			result.Outcome = agentshare.Completed
			result.Commit = agentshare.ReplyCommitInput{
				Persistence:  replyCommitter{state: s},
				Session:      s.session,
				Text:         s.sourceText,
				RawText:      s.sourceText,
				PlatformText: summary.Text,
				Stream:       summaryStream,
				Outputs:      later,
			}
			return result
		}
		if !toolPhase {
			if !s.route.Turns.StartToolPhase(s.session.ID, turn.AttemptFromContext(s.ctx)) {
				result.Outcome = agentshare.Stopped
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
			result.Outcome = agentshare.Stopped
			return result
		}
		if execution.ConfirmationExtra != "" {
			message := &storage.Message{ID: storage.NewID(), SessionID: s.session.ID, Role: storage.RoleUser, Content: "补充：" + execution.ConfirmationExtra}
			if _, err := (inputCommitter{state: s}).Commit(s.requestCtx, message, nil); err != nil {
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
		Identity: deps.Identity, SelectionForTurn: deps.SelectionForTurn,
		Gate: deps.Gate, SessionRows: deps.SessionRows,
		Status:     deps.Status,
		Repository: deps.Repository,
		Context:    nativeContext,
		compactor:  &Compactor{Context: nativeContext, Gate: deps.Gate, View: deps.View, System: deps.System, Identity: deps.Identity},
		Turns:      deps.Turns,
		View:       deps.View,
		Preparer:   deps.Preparer,
		Tools:      deps.Tools,
		Messages:   deps.Messages,
		Calls:      deps.Calls,
		System:     deps.System,
	}
}

func (r *Loop) Compactor() contextmgr.Compactor    { return r.compactor }
func (r *Loop) Material() session.MaterialPreparer { return r.Context }
