package dialogue

import (
	"context"
	"errors"
	"fmt"

	"elbot/internal/agent/events"
	"elbot/internal/contextmgr"
	"elbot/internal/media"
	"elbot/internal/memory/resident"
	"elbot/internal/modelmgr"
	"elbot/internal/request"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/toolrun"
	"elbot/internal/turn"
)

type Runner struct {
	Status interface {
		Record(context.Context, runtimestatus.Snapshot)
	}
	Calls    *CallProcessor
	System   SystemPromptManager
	Routes   LoopResolver
	Preparer *Preparer
	Messages *MessageStore
	Replies  *ReplyCommitter
	Turns    *turn.Manager
	View     ExecutionView
}
type PreparedTurn struct {
	route Loop
	input TurnInput
}

// CheckSelection validates route availability before compaction or input writes.
func (r *Runner) CheckSelection(selection modelmgr.Selection) error {
	loop, err := r.Routes.LoopFor(selection.Origin.APIType)
	if err != nil {
		return fmt.Errorf("provider %q 主对话尚未接入或接线无效：%w", selection.Provider, err)
	}
	return loop.CheckSelection(selection)
}

func (r *Runner) PrepareTurn(ctx context.Context, in TurnInput) (*PreparedTurn, error) {
	route, err := r.Routes.LoopFor(in.Selection.Origin.APIType)
	if err != nil {
		return nil, err
	}
	ctx, err = r.View.WithModel(ctx, in.Selection)
	if err != nil {
		return nil, err
	}
	return &PreparedTurn{route: route, input: in}, nil
}

func (r *Runner) RunTurn(ctx, requestCtx context.Context, in TurnInput, out Output) (result TurnResult) {
	result.StartedAt = storage.Now()
	result.Selection = in.Selection
	defer func() {
		if result.Err != nil && requestCtx.Err() != nil {
			result.QuietCancellation = true
			pending := []error{result.Err}
			for len(pending) > 0 {
				err := pending[len(pending)-1]
				pending = pending[:len(pending)-1]
				if joined, ok := err.(interface{ Unwrap() []error }); ok {
					pending = append(pending, joined.Unwrap()...)
					continue
				}
				if cause := errors.Unwrap(err); cause != nil {
					pending = append(pending, cause)
					continue
				}
				if !errors.Is(err, requestCtx.Err()) {
					result.QuietCancellation = false
					break
				}
			}
			if result.QuietCancellation {
				result.Outcome = Canceled
			}
		}
		if result.Outcome == Completed {
			return
		}
		execution := r.Turns.Execution(in.Session.ID)
		if execution != nil && execution == turn.ExecutionFromContext(ctx) && r.Turns.Snapshot(in.Session.ID).Phase == turn.PhaseAwaitAppendConfirm {
			result.Outcome = Paused
		} else if execution != nil && !r.Turns.MatchesAttempt(in.Session.ID, turn.AttemptFromContext(ctx)) {
			result.Outcome = Superseded
		}
	}()
	var err error
	ctx, err = r.View.WithModel(ctx, in.Selection)
	if err == nil {
		requestCtx, err = r.View.WithModel(requestCtx, in.Selection)
	}
	if err != nil {
		result.Outcome, result.Err = Failed, err
		return result
	}
	loopIn := LoopInput{Session: in.Session, Text: in.Text, Selection: in.Selection, RequestID: in.RequestID, StartedAt: result.StartedAt}
	materials, err := r.Preparer.LoadMaterials(requestCtx, in.Prepared.input)
	var prepared PreparedLoop
	if err == nil {
		prepared, err = in.Prepared.route.PrepareTurn(requestCtx, materials)
	}
	var user *storage.Message
	if err == nil {
		user, err = prepared.PrepareInput(ctx, requestCtx, loopIn, out)
	}
	if err == nil {
		err = requestCtx.Err()
	}
	if err == nil {
		_, err = prepared.InputCommitter().Commit(requestCtx, user, nil)
	}
	if err != nil {
		result.Outcome, result.Err = Failed, err
		return result
	}
	loop := prepared.RunLoop(ctx, requestCtx, loopIn, out)
	result.Outcome, result.Err, result.QuietCancellation = loop.Outcome, loop.Err, loop.QuietCancellation
	result.Usage, result.Selection = loop.Usage, loop.Selection
	if loop.Outcome != Completed {
		return result
	}
	requestCtx, err = r.View.RefreshSession(requestCtx, in.Session)
	if err != nil {
		result.Outcome, result.Err = Failed, err
		return result
	}
	ctx = r.View.Context(ctx)
	ctx, err = r.View.WithModel(ctx, result.Selection)
	if err == nil {
		requestCtx, err = r.View.WithModel(requestCtx, result.Selection)
	}
	if err != nil {
		result.Outcome, result.Err = Failed, err
		return result
	}
	r.Status.Record(ctx, runtimestatus.Snapshot{SessionID: in.Session.ID, Phase: runtimestatus.PhaseSending,
		Provider: result.Selection.Provider, Model: result.Selection.Model, Mode: in.Session.Mode, RequestID: in.RequestID,
		Kind: request.KindTurn, Label: "chat", TurnStartedAt: result.StartedAt, StageStartedAt: storage.Now()})
	result.Committed, err = r.Replies.Commit(ctx, requestCtx, loop.Commit, out)
	if err != nil {
		result.Outcome, result.Err = Failed, err
	}
	return result
}

// RunnerDeps contains external capabilities needed to build the common single-turn collaborators.
type RunnerDeps struct {
	Status interface {
		Record(context.Context, runtimestatus.Snapshot)
	}
	Routes          LoopResolver
	Dialogues       storage.DialogueRepository
	Sessions        *session.Service
	Turns           *turn.Manager
	Contexts        *contextmgr.Service
	Media           *media.Manager
	Identity        Identity
	Hooks           Hooks
	Output          AssistantPreparer
	ToolManager     *toolrun.Manager
	ToolRegistry    *tool.Registry
	ToolState       *toolrun.StateService
	ToolDeps        ToolDepsFactory
	ToolProvider    ToolSchemaProvider
	DefaultProvider bool
	// MaxRounds bounds tool batches within one turn.
	MaxRounds int
	SoulPath  string
	Memory    *resident.Store
	Preloader *toolrun.PreloadService
	Signals   events.Signals
	View      ExecutionView
}

func NewRunner(deps RunnerDeps) *Runner {
	tools := &ToolExecutor{Manager: deps.ToolManager, State: deps.ToolState, Registry: deps.ToolRegistry, Provider: deps.ToolProvider, DefaultProvider: deps.DefaultProvider, Identity: deps.Identity, Deps: deps.ToolDeps, MaxRounds: deps.MaxRounds}
	messages := &MessageStore{Dialogues: deps.Dialogues, Gate: &CommitGate{Sessions: deps.Sessions, Turns: deps.Turns, View: deps.View}}
	tools.Messages = messages
	replies := &ReplyCommitter{Output: deps.Output, Persistence: messages.Committer("append_assistant_message")}
	preparer := &Preparer{Contexts: deps.Contexts, Media: deps.Media, Identity: deps.Identity, Hooks: deps.Hooks, Tools: tools}
	messages.Preparer = preparer
	calls := &CallProcessor{Messages: messages, Preparer: preparer, Hooks: deps.Hooks, Identity: deps.Identity, Tools: tools, Completed: deps.Signals.ModelCallCompleted, Vision: deps.Signals.VisionFallbackUsed}
	soul := SoulProvider(StaticSoulProvider{Prompt: "You are a helpful assistant."})
	if deps.SoulPath != "" {
		soul = &FileSoulProvider{Path: deps.SoulPath}
	}
	system := NewSystemPromptManager(SoulSystemPromptSource{Soul: soul})
	if names, ok := deps.ToolProvider.(ToolNameProvider); ok {
		system.AddSource(ToolNamesSystemPromptSource{Tools: names})
	}
	system.AddSource(ToolTagsSystemPromptSource{Preloader: deps.Preloader})

	system.AddSource(BackgroundPathsSystemPromptSource{})
	system.AddSource(ResidentMemorySystemPromptSource{Store: deps.Memory})
	system.AddSource(ConversationMetaSystemPromptSource{})
	return &Runner{Status: deps.Status, Routes: deps.Routes, Preparer: preparer, Messages: messages, Replies: replies, Turns: deps.Turns, View: deps.View, Calls: calls, System: system}
}
