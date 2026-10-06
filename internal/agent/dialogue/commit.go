package dialogue

import (
	"context"
	"encoding/json"
	"fmt"

	"elbot/internal/llm"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

// MessageCommitter commits a prepared business message, or a cursor-only reply.
// Protocol-private implementations can atomically attach their native state.
type MessageCommitter interface {
	Commit(context.Context, *storage.Message) error
}

// CommitGate uses the original activation and live attempt, not public IDs as
// authorization. It is held only for local persistence, never across external IO.
type CommitGate struct {
	Sessions *session.Service
	Turns    *turn.Manager
	View     ExecutionView
}

func (g *CommitGate) Enter(ctx context.Context, sessionID string) (context.Context, func(), error) {
	if err := ctx.Err(); err != nil {
		return ctx, nil, err
	}
	if g == nil {
		return ctx, func() {}, nil
	}
	ctx = g.View.Context(ctx)
	var release func()
	var err error
	if binding, ok := session.BindingFromContext(ctx); ok {
		if binding.SessionID() != sessionID {
			return ctx, nil, fmt.Errorf("message session binding mismatch")
		}
		ctx, release, err = g.Sessions.EnterBinding(ctx, binding)
	} else {
		ctx, release, err = g.Sessions.EnterSessions(ctx, sessionID)
	}
	if err != nil {
		return ctx, nil, err
	}
	if execution := turn.ExecutionFromContext(ctx); execution != nil {
		if g.Turns.Execution(sessionID) != execution || !g.Turns.MatchesAttempt(sessionID, turn.AttemptFromContext(ctx)) {
			release()
			return ctx, nil, fmt.Errorf("message execution attempt expired")
		}
	}
	return ctx, release, nil
}

type messageCommitter struct {
	store     *MessageStore
	operation string
}

func (s *MessageStore) Committer(operation string) MessageCommitter {
	return messageCommitter{store: s, operation: operation}
}

func (c messageCommitter) Commit(ctx context.Context, message *storage.Message) error {
	if message == nil {
		return nil
	}
	return c.store.Append(ctx, message, c.operation)
}

// ToolTranscriptCommitter retains a single batch head while committing each
// prepared call and result before advancing the shared ToolRun.
type ToolTranscriptCommitter struct {
	Messages  *MessageStore
	HeadID    string
	SessionID string
}

func (c *ToolTranscriptCommitter) Begin(ctx context.Context, head *storage.Message) error {
	if err := c.Messages.Append(ctx, head, "append_tool_transcript"); err != nil {
		return err
	}
	c.HeadID, c.SessionID = head.ID, head.SessionID
	return nil
}
func (c *ToolTranscriptCommitter) Prepared(ctx context.Context, index int, call llm.ToolCallRequest) error {
	raw, err := json.Marshal(call)
	if err != nil {
		return err
	}
	return c.Messages.Commit(ctx, storage.DialogueCommit{SessionID: c.SessionID, ToolCall: &storage.ToolCallUpdate{MessageID: c.HeadID, Index: index, Call: raw}}, "update_tool_transcript")
}
func (c *ToolTranscriptCommitter) Started(context.Context, int, llm.ToolCallRequest) error {
	return nil
}
func (c *ToolTranscriptCommitter) Result(ctx context.Context, _ int, _ llm.ToolCallRequest, _ llm.LLMMessage, stored *storage.Message) error {
	return c.Messages.Append(ctx, stored, "append_tool_transcript")
}
