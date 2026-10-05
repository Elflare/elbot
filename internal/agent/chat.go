package agent

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"elbot/internal/contextmgr"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/modelmgr"
	"elbot/internal/request"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/storage"
	"elbot/internal/toolrun"
	"elbot/internal/turn"
)

type chatRunner struct {
	messages      storage.MessageRepository
	media         *media.Manager
	contexts      *contextmgr.Service
	models        *modelmgr.Service
	turns         *turn.Manager
	identity      *identityResolver
	hooks         *hookBridge
	view          executionView
	promptBuilder PromptBuilder
	toolRuntime   *toolRuntimeState
	toolState     *toolrun.StateService
	toolDeps      *toolRunDeps
	caller        *modelCaller
	replies       *replyCommitter
	logger        *slog.Logger
	auditLogger   *slog.Logger
}

type chatTurnOutcome uint8

const (
	chatTurnCompleted chatTurnOutcome = iota
	chatTurnPaused
	chatTurnStopped
	chatTurnCanceled
	chatTurnFailed
	chatTurnSuperseded
)

type chatTurnInput struct {
	Session   *storage.Session
	Text      string
	Selection modelmgr.Selection
	RequestID string
	Prepared  *preparedTurn
}

// QuietCancellation preserves cancellation handled at a request boundary: it
// finishes the logical execution without returning an additional user error.
type chatTurnResult struct {
	Outcome           chatTurnOutcome
	Err               error
	QuietCancellation bool
	Committed         replyCommitResult
	Usage             *llm.Usage
	Selection         modelmgr.Selection
	StartedAt         time.Time
}

type chatTurnState struct {
	ctx        context.Context
	requestCtx context.Context
	session    *storage.Session
	text       string
	output     turnOutput
	selection  modelmgr.Selection
	requestID  string
	startedAt  time.Time
	messages   []llm.LLMMessage
	tools      []llm.ToolSchema
	usage      *llm.Usage
}

func failedChatOutcome(err error) chatTurnOutcome {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return chatTurnCanceled
	}
	return chatTurnFailed
}

func (r *chatRunner) RunTurn(ctx, requestCtx context.Context, in chatTurnInput, out turnOutput) (result chatTurnResult) {
	s := &chatTurnState{ctx: ctx, requestCtx: requestCtx, session: in.Session, text: in.Text, output: out,
		selection: in.Selection, requestID: in.RequestID, startedAt: storage.Now()}
	defer func() {
		result.Usage, result.Selection, result.StartedAt = s.usage, s.selection, s.startedAt
		// A committed reply remains a successful turn even if input interrupts
		// during output. The coordinator still records usage before handing off.
		if result.Outcome == chatTurnCompleted {
			return
		}
		execution := r.turns.Execution(in.Session.ID)
		if execution != nil && execution == turn.ExecutionFromContext(ctx) && r.turns.Snapshot(in.Session.ID).Phase == turn.PhaseAwaitAppendConfirm {
			result.Outcome = chatTurnPaused
		} else if execution != nil && !r.turns.MatchesAttempt(in.Session.ID, turn.AttemptFromContext(ctx)) {
			result.Outcome = chatTurnSuperseded
		}
	}()
	if err := r.prepareMessages(s, in.Prepared); err != nil {
		return chatTurnResult{Outcome: failedChatOutcome(err), Err: err}
	}
	loop := r.runLoop(s)
	result.Outcome, result.Err, result.QuietCancellation = loop.Outcome, loop.Err, loop.QuietCancellation
	if loop.Outcome != chatTurnCompleted {
		return result
	}
	var err error
	s.requestCtx, err = r.view.RefreshSession(s.requestCtx, s.session)
	if err != nil {
		result.Outcome, result.Err = failedChatOutcome(err), err
		return result
	}
	s.ctx = r.view.Context(s.ctx)
	out.PublishRuntimeStatus(s.ctx, runtimestatus.Snapshot{SessionID: s.session.ID, Phase: runtimestatus.PhaseSending,
		Provider: s.selection.Provider, Model: s.selection.Model, Mode: s.session.Mode, RequestID: s.requestID,
		Kind: request.KindTurn, Label: "chat", TurnStartedAt: s.startedAt, StageStartedAt: storage.Now()})
	result.Committed, err = r.replies.Commit(s.ctx, s.requestCtx, loop.Commit, out)
	if err != nil {
		result.Outcome, result.Err = failedChatOutcome(err), err
	}
	return result
}
