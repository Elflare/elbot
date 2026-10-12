package dialogue

import (
	"context"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/contextmgr"
	"elbot/internal/events"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

type Identity interface {
	Scope(context.Context) session.Scope
	Actor(context.Context) contextinfo.Actor
	IsCLI(context.Context) bool
}
type Hooks interface {
	Run(context.Context, hook.Event) (hook.Event, events.LogResult, error)
	Notify(context.Context, hook.Event)
}

type Outcome uint8

const (
	Completed Outcome = iota
	Paused
	Stopped
	Canceled
	Failed
	Superseded
)

type TurnInput struct {
	Session                  *storage.Session
	Text                     string
	Input                    turn.Input
	ReplyToPlatformMessageID string
	Selection                modelmgr.Selection
	RequestID                string
	Prepared                 *PreparedTurn
}
type TurnMaterials struct {
	Session     *storage.Session
	Input       turn.Input
	UserMessage *storage.Message
	Loaded      *contextmgr.LoadedContext
}
type LoopInput struct {
	Session   *storage.Session
	Text      string
	Selection modelmgr.Selection
	RequestID string
	StartedAt time.Time
}

// Each prepared loop keeps its own typed protocol state for this turn.
type Loop interface {
	CheckSelection(modelmgr.Selection) error
	PrepareTurn(context.Context, TurnMaterials) (PreparedLoop, error)
}
type PreparedLoop interface {
	InputCommitter() MessageCommitter
	PrepareInput(context.Context, context.Context, LoopInput, Output) (*storage.Message, error)
	RunLoop(context.Context, context.Context, LoopInput, Output) LoopResult
}
type LoopResolver interface {
	LoopFor(llm.APIType) (Loop, error)
}

type LoopResult struct {
	Outcome           Outcome
	Err               error
	QuietCancellation bool
	Commit            ReplyCommitInput
	Usage             *llm.Usage
	Selection         modelmgr.Selection
}
type TurnResult struct {
	Outcome           Outcome
	Err               error
	QuietCancellation bool
	Committed         ReplyCommitResult
	Usage             *llm.Usage
	Selection         modelmgr.Selection
	StartedAt         time.Time
}
