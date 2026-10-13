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

// ToolTranscriptCommitter buffers one complete batch before publishing its projection.
type ToolTranscriptCommitter struct {
	Messages  *MessageStore
	SessionID string
	Committed []llm.LLMMessage
	head      *storage.Message
	calls     []llm.ToolCallRequest
	results   []*storage.Message
}

func (c *ToolTranscriptCommitter) Begin(_ context.Context, head *storage.Message) (events.LogResult, error) {
	c.SessionID, c.head = head.SessionID, head
	c.calls = append([]llm.ToolCallRequest(nil), AssistantMessageMetadata(head.Metadata).ToolCalls...)
	c.results = make([]*storage.Message, len(c.calls))
	return events.LogResult{ResultStatus: events.ResultSkipped}, nil
}
func (c *ToolTranscriptCommitter) Prepared(_ context.Context, index int, call llm.ToolCallRequest) (events.LogResult, error) {
	c.calls[index] = call
	return events.LogResult{ResultStatus: events.ResultSkipped}, nil
}
func (c *ToolTranscriptCommitter) Started(context.Context, int, llm.ToolCallRequest) (events.LogResult, error) {
	return events.LogResult{ResultStatus: events.ResultSkipped}, nil
}
func (c *ToolTranscriptCommitter) Result(_ context.Context, index int, call llm.ToolCallRequest, _ llm.LLMMessage, stored *storage.Message) (events.LogResult, error) {
	c.calls[index], c.results[index] = call, stored
	return events.LogResult{ResultStatus: events.ResultSkipped}, nil
}
func (c *ToolTranscriptCommitter) Flush(ctx context.Context) (events.LogResult, error) {
	batch := NewToolBatch(c.head, c.calls, c.results)
	result, err := c.Messages.Commit(ctx, storage.DialogueCommit{SessionID: c.SessionID, ToolBatch: batch}, "append_tool_transcript")
	if err == nil {
		c.Committed = append(c.Committed, MessageFromStorage(*batch.Call))
		for _, stored := range batch.Results {
			c.Committed = append(c.Committed, MessageFromStorage(*stored))
		}
	}
	return result, err
}

// NewToolBatch retains one assistant call array and ordered call/result links.
func NewToolBatch(head *storage.Message, calls []llm.ToolCallRequest, results []*storage.Message) *storage.ToolBatch {
	if head.ID == "" {
		head.ID = storage.NewID()
	}
	head.CreatedAt = storage.Now()
	ids := make([]string, len(results))
	for i, result := range results {
		if result.ID == "" {
			result.ID = storage.NewID()
		}
		ids[i] = result.ID
	}
	fields, _ := storage.DecodeSessionMetadata(head.Metadata)
	_ = fields.Set("tool_calls", calls)
	_ = fields.Set(storage.ToolResultMessageKey, ids)
	head.Metadata, _ = fields.Encode()
	return &storage.ToolBatch{Call: head, Results: results}
}
