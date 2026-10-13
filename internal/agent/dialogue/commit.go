package dialogue

import (
	"context"
	"fmt"

	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

// MessageCommitter commits a prepared business message, or a cursor-only reply.
// Protocol-private implementations can atomically attach their native state.
type MessageCommitter interface {
	Commit(context.Context, *storage.Message) (events.LogResult, error)
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

func (c messageCommitter) Commit(ctx context.Context, message *storage.Message) (events.LogResult, error) {
	if message == nil {
		return events.LogResult{ResultStatus: events.ResultSkipped}, nil
	}
	return c.store.Append(ctx, message, c.operation)
}

// ToolTranscriptCommitter commits complete business facts and exposes only the
// successfully committed projection to the active loop.
type ToolTranscriptCommitter struct {
	Messages  *MessageStore
	SessionID string
	Committed []llm.LLMMessage
}

func (c *ToolTranscriptCommitter) Begin(ctx context.Context, head *storage.Message) (events.LogResult, error) {
	c.SessionID = head.SessionID
	message := ToolBatchText(head)
	result, err := c.Messages.Append(ctx, message, "append_tool_transcript")
	if err == nil && message != nil {
		c.Committed = append(c.Committed, MessageFromStorage(*message))
	}
	return result, err
}
func (c *ToolTranscriptCommitter) Prepared(context.Context, int, llm.ToolCallRequest) (events.LogResult, error) {
	return events.LogResult{ResultStatus: events.ResultSkipped}, nil
}
func (c *ToolTranscriptCommitter) Started(context.Context, int, llm.ToolCallRequest) (events.LogResult, error) {
	return events.LogResult{ResultStatus: events.ResultSkipped}, nil
}
func (c *ToolTranscriptCommitter) Result(ctx context.Context, _ int, call llm.ToolCallRequest, _ llm.LLMMessage, stored *storage.Message) (events.LogResult, error) {
	pair := NewToolPair(c.SessionID, call, stored)
	result, err := c.Messages.Commit(ctx, storage.DialogueCommit{SessionID: c.SessionID, ToolPair: pair}, "append_tool_transcript")
	if err == nil {
		c.Committed = append(c.Committed, MessageFromStorage(*pair.Call), MessageFromStorage(*pair.Result))
	}
	return result, err
}

func ToolBatchText(head *storage.Message) *storage.Message {
	if head.Content == "" {
		return nil
	}
	metadata := AssistantMessageMetadata(head.Metadata)
	message := ToolCallStorageMessage(head.SessionID, head.Content, metadata.RawText, nil)
	message.ID = storage.NewID()
	return &message
}

func NewToolPair(sessionID string, call llm.ToolCallRequest, result *storage.Message) *storage.ToolPair {
	if result.ID == "" {
		result.ID = storage.NewID()
	}
	head := ToolCallStorageMessage(sessionID, "", "", []llm.ToolCallRequest{call})
	head.ID = storage.NewID()
	fields, _ := storage.DecodeSessionMetadata(head.Metadata)
	_ = fields.Set(storage.ToolResultMessageKey, result.ID)
	head.Metadata, _ = fields.Encode()
	head.CreatedAt = storage.Now()
	result.CreatedAt = head.CreatedAt
	return &storage.ToolPair{Call: &head, Result: result}
}
