package dialogue

import (
	"context"
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

// ToolTranscriptCommitter commits each completed display-history pair.
type ToolTranscriptCommitter struct {
	Messages  *MessageStore
	SessionID string
}

func (c *ToolTranscriptCommitter) Begin(ctx context.Context, head *storage.Message) error {
	c.SessionID = head.SessionID
	return c.Messages.Append(ctx, ToolBatchText(head), "append_tool_transcript")
}
func (c *ToolTranscriptCommitter) Prepared(context.Context, int, llm.ToolCallRequest) error {
	return nil
}
func (c *ToolTranscriptCommitter) Started(context.Context, int, llm.ToolCallRequest) error {
	return nil
}
func (c *ToolTranscriptCommitter) Result(ctx context.Context, _ int, call llm.ToolCallRequest, _ llm.LLMMessage, stored *storage.Message) error {
	return c.Messages.Commit(ctx, storage.DialogueCommit{SessionID: c.SessionID, ToolPair: NewToolPair(c.SessionID, call, stored)}, "append_tool_transcript")
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
