package dialogue

import (
	"context"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/contextinfo"
	"elbot/internal/contextmgr"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/memory/resident"
	"elbot/internal/modelmgr"
	"elbot/internal/operationresult"
	"elbot/internal/request"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"fmt"
	"log/slog"
	"sync"

	"elbot/internal/delivery"
	"elbot/internal/turn"
)

type attemptOutput struct {
	agentshare.Output
	mu     sync.Mutex
	stream *attemptMessageStream
}

type attemptMessageStream struct {
	delivery.MessageStream
	mu        sync.Mutex
	committed bool
}

func (s *attemptMessageStream) Send(ctx context.Context, event delivery.StreamEvent) (delivery.Receipt, error) {
	receipt, err := s.MessageStream.Send(ctx, event)
	if err == nil {
		s.mu.Lock()
		switch event.Type {
		case delivery.StreamSend:
			s.committed = true
		case delivery.StreamReset:
			s.committed = false
		}
		s.mu.Unlock()
	}
	return receipt, err
}

func (o *attemptOutput) StartStream(ctx context.Context) delivery.MessageStream {
	stream := &attemptMessageStream{MessageStream: o.Output.StartStream(ctx)}
	o.mu.Lock()
	o.stream = stream
	o.mu.Unlock()
	return stream
}

func (o *attemptOutput) reset(ctx context.Context) error {
	o.mu.Lock()
	stream := o.stream
	o.mu.Unlock()
	if stream == nil {
		return nil
	}
	stream.mu.Lock()
	committed := stream.committed
	stream.mu.Unlock()
	if committed {
		return nil
	}
	_, err := stream.Send(context.WithoutCancel(ctx), delivery.StreamEvent{Type: delivery.StreamReset})
	return err
}

type Runner struct {
	Status interface {
		Record(context.Context, runtimestatus.Snapshot)
	}
	Calls    *CallProcessor
	System   SystemPromptManager
	Routes   agentshare.LoopResolver
	Preparer *Preparer
	Messages *MessageStore
	Replies  *ReplyCommitter
	Turns    interface {
		Snapshot(string) turn.Snapshot
	}
	View ExecutionView
}
type PreparedTurn struct {
	route    agentshare.Loop
	input    agentshare.TurnInput
	preparer *Preparer
}

// CheckSelection validates route availability before compaction or input writes.
func (r *Runner) CheckSelection(selection modelmgr.Selection) (operationresult.Result[struct{}], error) {
	if selection.Client == nil {
		return operationresult.Result[struct{}]{Status: operationresult.NotFound, Message: fmt.Sprintf("model provider %q not found", selection.Provider)}, nil
	}
	loop, err := r.Routes.LoopFor(selection.Origin.APIType)
	if err != nil {
		return operationresult.Result[struct{}]{}, fmt.Errorf("provider %q 主对话尚未接入或接线无效：%w", selection.Provider, err)
	}
	return loop.CheckSelection(selection), nil
}

func (r *Runner) PrepareTurn(ctx context.Context, in agentshare.TurnInput) (agentshare.PreparedTurn, error) {
	route, err := r.Routes.LoopFor(in.Selection.Origin.APIType)
	if err != nil {
		return nil, err
	}
	ctx, err = r.View.WithModel(ctx, in.Selection)
	if err != nil {
		return nil, err
	}
	return &PreparedTurn{route: route, input: in, preparer: r.Preparer}, nil
}

func (r *Runner) RunTurn(ctx, requestCtx context.Context, in agentshare.TurnInput, out agentshare.Output) (requestContext context.Context, operation operationresult.Result[agentshare.TurnResult], runErr error) {
	var result agentshare.TurnResult
	streamOutput := &attemptOutput{Output: out}
	out = streamOutput
	result.StartedAt = storage.Now()
	result.Selection = in.Selection
	defer func() {
		if result.Outcome != agentshare.Completed {
			current := r.Turns.Snapshot(in.Session.ID)
			execution := current.Execution
			if execution != nil && execution == turn.ExecutionFromContext(ctx) && current.Attempt == turn.AttemptFromContext(ctx) && current.Phase == turn.PhaseAwaitAppendConfirm {
				if err := streamOutput.reset(ctx); err != nil {
					slog.WarnContext(context.WithoutCancel(ctx), "reset interrupted reply stream", "session_id", in.Session.ID, "error", err)
				}
				result.Outcome = agentshare.Paused
			} else if execution != nil && current.Attempt != turn.AttemptFromContext(ctx) {
				result.Outcome = agentshare.Superseded
			}
		}
		if operation.Status == operationresult.Success {
			operation.Data = result
		}
	}()
	var err error
	ctx, err = r.View.WithModel(ctx, in.Selection)
	if err == nil {
		requestCtx, err = r.View.WithModel(requestCtx, in.Selection)
	}
	if err != nil {
		return requestCtx, operationresult.Result[agentshare.TurnResult]{}, err
	}
	loopIn := agentshare.LoopInput{Session: in.Session, Selection: in.Selection, RequestID: in.RequestID, StartedAt: result.StartedAt}
	preparation, err := in.Prepared.PrepareTurn(requestCtx)
	if err != nil {
		return requestCtx, operationresult.Result[agentshare.TurnResult]{}, err
	}
	if preparation.Status != operationresult.Success {
		result.Outcome = agentshare.Failed
		return requestCtx, operationresult.Result[agentshare.TurnResult]{Status: preparation.Status, Message: preparation.Message}, nil
	}
	prepared := preparation.Data
	input, err := prepared.PrepareInput(ctx, requestCtx, loopIn, out)
	if err != nil {
		return requestCtx, operationresult.Result[agentshare.TurnResult]{}, err
	}
	if input.Status != operationresult.Success {
		result.Outcome = agentshare.Failed
		return requestCtx, operationresult.Result[agentshare.TurnResult]{Status: input.Status, Message: input.Message}, nil
	}
	user := input.Data
	if err := requestCtx.Err(); err != nil {
		return requestCtx, operationresult.Result[agentshare.TurnResult]{}, err
	}
	if err := prepared.InputCommitter().Commit(requestCtx, user, nil); err != nil {
		return requestCtx, operationresult.Result[agentshare.TurnResult]{}, err
	}
	if user != nil {
		fields, _ := storage.DecodeSessionMetadata(user.Metadata)
		if string(fields["elyph_rules_shown"]) == "true" {
			fields, err := storage.DecodeSessionMetadata(in.Session.Metadata)
			if err != nil {
				return requestCtx, operationresult.Result[agentshare.TurnResult]{}, err
			}
			_ = fields.Set("elyph_rules_shown", true)
			in.Session.Metadata, _ = fields.Encode()
		}
	}
	executed, err := prepared.RunLoop(ctx, requestCtx, loopIn, out)
	if err != nil {
		return requestCtx, operationresult.Result[agentshare.TurnResult]{}, err
	}
	if executed.Status != operationresult.Success {
		result.Outcome = agentshare.Failed
		return requestCtx, operationresult.Result[agentshare.TurnResult]{Status: executed.Status, Message: executed.Message}, nil
	}
	loop := executed.Data
	result.Outcome = loop.Outcome
	result.Usage, result.Selection = loop.Usage, loop.Selection
	if loop.Outcome != agentshare.Completed {
		return requestCtx, operationresult.Result[agentshare.TurnResult]{Status: operationresult.Success, Data: result}, nil
	}
	requestCtx, err = r.View.RefreshSession(requestCtx, in.Session)
	if err != nil {
		return requestCtx, operationresult.Result[agentshare.TurnResult]{}, err
	}
	ctx = r.View.Context(ctx)
	ctx, err = r.View.WithModel(ctx, result.Selection)
	if err == nil {
		requestCtx, err = r.View.WithModel(requestCtx, result.Selection)
	}
	if err != nil {
		return requestCtx, operationresult.Result[agentshare.TurnResult]{}, err
	}
	r.Status.Record(ctx, runtimestatus.Snapshot{SessionID: in.Session.ID, Phase: runtimestatus.PhaseSending,
		Provider: result.Selection.Provider, Model: result.Selection.Model, Mode: in.Session.Mode, RequestID: in.RequestID,
		Kind: request.KindTurn, Label: "chat", TurnStartedAt: result.StartedAt, StageStartedAt: storage.Now()})
	result.Committed, err = r.Replies.Commit(ctx, requestCtx, loop.Commit, out)
	if err != nil {
		return requestCtx, operationresult.Result[agentshare.TurnResult]{}, err
	}
	return requestCtx, operationresult.Result[agentshare.TurnResult]{Status: operationresult.Success, Data: result}, nil
}

// RunnerDeps contains external capabilities needed to build the common single-turn collaborators.
type RunnerDeps struct {
	NewMessageID func(context.Context) (string, error) // 根部注入，供消息创建与提交共用。
	Status       interface {
		Record(context.Context, runtimestatus.Snapshot)
		RecordUsage(context.Context, string, *llm.Usage)
	}
	Routes    agentshare.LoopResolver
	Dialogues interface {
		Commit(context.Context, storage.DialogueCommit) error
	}
	Sessions interface {
		EnterBinding(context.Context, *session.Binding) (context.Context, func(), error)
		EnterSessions(context.Context, ...string) (context.Context, func(), error)
	}
	Turns interface {
		Snapshot(string) turn.Snapshot
	}
	Contexts interface {
		Load(context.Context, string) (*contextmgr.LoadedContext, error)
	}
	Media interface {
		AcquireForLLM(context.Context, []llm.LLMMessage) (operationresult.Result[[]llm.LLMMessage], media.UsageID, error)
	}
	PublishUsageFinished func(context.Context, media.UsageID) // 根部连接 Media 的结束信号。
	Identity             interface {
		Actor(context.Context) contextinfo.Actor
		IsCLI(context.Context) bool
	}
	Hooks interface {
		LLMTurnPrepared(context.Context, agentshare.Session, agentshare.LLMInput) (agentshare.LLMInputResult, error)
		LLMRequestPrepared(context.Context, agentshare.Session, agentshare.LLMInput) (agentshare.LLMInputResult, error)
		LLMResponseReceived(context.Context, agentshare.Session, agentshare.LLMResponse) (agentshare.LLMResponseResult, error)
		ErrorOccurred(context.Context, agentshare.Session, agentshare.LLMResponse, error)
	}
	Output      AssistantPreparer
	ToolManager interface {
		QueryTools(context.Context, tool.ToolQuery) ([]tool.ToolInfo, error)
		FilterSchemas(context.Context, []llm.ToolSchema) ([]llm.ToolSchema, error)
		Run(context.Context, tool.RunnerDeps, tool.RunRequest) tool.RunResult
		Schemas(context.Context, tool.Context, []tool.DiscoveredTool) ([]llm.ToolSchema, error)
		DiscoveredToolsForSession(context.Context, string, bool) ([]tool.DiscoveredTool, error)
		DecodeState(string) (tool.State, error)
	}
	ToolDeps     agentshare.ToolDepsFactory
	ToolProvider agentshare.ToolNameProvider
	// MaxRounds bounds tool batches within one turn.
	MaxRounds int
	SoulPath  string
	Memory    interface {
		Read(context.Context, session.Scope) (resident.Memory, error)
	}
	Preloader interface {
		TagPrompts(context.Context, []string) ([]tool.TagPrompt, error)
		DecodeState(string) (tool.State, error)
	}
	View ExecutionView
}

func NewRunner(deps RunnerDeps) *Runner {
	tools := &ToolExecutor{Manager: deps.ToolManager, Identity: deps.Identity, Deps: deps.ToolDeps, MaxRounds: deps.MaxRounds}
	messages := &MessageStore{NewMessageID: deps.NewMessageID, Dialogues: deps.Dialogues, Gate: &CommitGate{Sessions: deps.Sessions, Turns: deps.Turns, View: deps.View}}
	replies := &ReplyCommitter{NewMessageID: deps.NewMessageID, Output: deps.Output, Persistence: messages.Committer("append_assistant_message")}
	preparer := &Preparer{NewMessageID: deps.NewMessageID, Contexts: deps.Contexts, Media: deps.Media, PublishUsageFinished: deps.PublishUsageFinished, Hooks: deps.Hooks, Tools: tools}
	messages.Preparer = preparer
	calls := &CallProcessor{Status: deps.Status, Preparer: preparer, Hooks: deps.Hooks, Tools: tools}
	soul := SoulProvider(StaticSoulProvider{Prompt: "You are a helpful assistant."})
	if deps.SoulPath != "" {
		soul = &FileSoulProvider{Path: deps.SoulPath}
	}
	system := NewSystemPromptManager(SoulSystemPromptSource{Soul: soul})
	system.AddSource(ToolNamesSystemPromptSource{Tools: deps.ToolProvider})
	system.AddSource(ToolTagsSystemPromptSource{Preloader: deps.Preloader})

	system.AddSource(BackgroundPathsSystemPromptSource{})
	system.AddSource(ResidentMemorySystemPromptSource{Store: deps.Memory})
	system.AddSource(ConversationMetaSystemPromptSource{})
	return &Runner{Status: deps.Status, Routes: deps.Routes, Preparer: preparer, Messages: messages, Replies: replies, Turns: deps.Turns, View: deps.View, Calls: calls, System: system}
}

// PrepareTurn delays material loading until the execution attempt is admitted.
func (p *PreparedTurn) PrepareTurn(ctx context.Context) (operationresult.Result[agentshare.PreparedLoop], error) {
	materials, err := p.preparer.LoadMaterials(ctx, p.input)
	if err != nil {
		return operationresult.Result[agentshare.PreparedLoop]{}, err
	}
	return p.route.PrepareTurn(ctx, materials)
}
