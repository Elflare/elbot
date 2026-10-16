package dialogue

import (
	"context"
	"fmt"
	"slices"

	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

// MessageCommitter commits a prepared business message, or a cursor-only reply.
// Protocol-private implementations can atomically attach their native state.
type MessageCommitter interface {
	Commit(context.Context, *storage.Message, []storage.PlatformMessageMap) (events.LogResult, error)
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

func (c messageCommitter) Commit(ctx context.Context, message *storage.Message, mappings []storage.PlatformMessageMap) (events.LogResult, error) {
	if message == nil {
		return events.LogResult{ResultStatus: events.ResultSkipped}, nil
	}
	return c.store.Commit(ctx, storage.DialogueCommit{SessionID: message.SessionID, Messages: []*storage.Message{message}, PlatformMessageMaps: mappings}, c.operation)
}

// ToolTranscriptCommitter reserves ordered slots and commits each final result.
type ToolTranscriptCommitter struct {
	Messages  *MessageStore
	SessionID string
	Committed []llm.LLMMessage
	head      *storage.Message
	calls     []llm.ToolCallRequest
	results   []*storage.Message
	remaining int
}

func (c *ToolTranscriptCommitter) Begin(ctx context.Context, head *storage.Message) (events.LogResult, error) {
	c.SessionID, c.head = head.SessionID, head
	metadata, err := AssistantMessageMetadata(head.Metadata)
	if err != nil {
		return events.LogResult{ResultStatus: events.ResultSkipped}, err
	}
	c.calls = append([]llm.ToolCallRequest(nil), metadata.ToolCalls...)
	c.results = make([]*storage.Message, len(c.calls))
	c.remaining = len(c.calls)
	for i, call := range c.calls {
		stored := ToolResultStorageMessage(c.SessionID, llm.LLMMessage{Role: llm.RoleTool, Name: call.Name, ToolCallID: call.ID, Segments: llm.TextSegments("tool outcome is unknown: result has not been saved; do not assume success or retry automatically")})
		fields, _ := storage.DecodeSessionMetadata(stored.Metadata)
		_ = fields.Set("tool_result_status", "pending")
		stored.Metadata, _ = fields.Encode()
		c.results[i] = &stored
	}
	batch, err := NewToolBatch(head, c.calls, c.results)
	if err != nil {
		return events.LogResult{ResultStatus: events.ResultSkipped}, err
	}
	fields, _ := storage.DecodeSessionMetadata(head.Metadata)
	_ = fields.Set("tool_batch_status", "pending")
	head.Metadata, _ = fields.Encode()
	return c.Messages.Commit(ctx, storage.DialogueCommit{SessionID: c.SessionID, ToolBatch: batch}, "begin_tool_transcript")
}
func (c *ToolTranscriptCommitter) Prepared(context.Context, int, llm.ToolCallRequest) (events.LogResult, error) {
	return events.LogResult{ResultStatus: events.ResultSkipped}, nil
}
func (c *ToolTranscriptCommitter) Result(ctx context.Context, index int, call llm.ToolCallRequest, _ llm.LLMMessage, update *storage.ToolResultCommit) (events.LogResult, error) {
	commit := storage.DialogueCommit{SessionID: c.SessionID, ToolResult: update}
	if call.Arguments != c.calls[index].Arguments {
		previous := media.ArgumentMediaIDs(c.calls[index].Arguments)
		for _, id := range media.ArgumentMediaIDs(call.Arguments) {
			if !slices.Contains(previous, id) {
				commit.MediaReferences = append(commit.MediaReferences, storage.MediaReference{MediaID: id, OwnerType: "session_tool", OwnerID: c.SessionID, SessionID: c.SessionID, Purpose: "input", CreatedAt: storage.Now()})
			}
		}
	}
	stored := update.Result
	stored.ID, stored.CreatedAt = c.results[index].ID, c.results[index].CreatedAt
	c.calls[index], c.results[index] = call, stored
	fields, _ := storage.DecodeSessionMetadata(stored.Metadata)
	_ = fields.Set("tool_result_status", "completed")
	stored.Metadata, _ = fields.Encode()
	fields, _ = storage.DecodeSessionMetadata(c.head.Metadata)
	_ = fields.Set("tool_calls", c.calls)
	if c.remaining == 1 {
		_ = fields.Set("tool_batch_status", "completed")
	}
	c.head.Metadata, _ = fields.Encode()
	update.Call = c.head
	result, err := c.Messages.Commit(context.WithoutCancel(ctx), commit, "save_tool_result")
	if err == nil {
		c.remaining--
	}
	return result, err
}
func (c *ToolTranscriptCommitter) Flush(ctx context.Context) (events.LogResult, error) {
	result := events.LogResult{ResultStatus: events.ResultSkipped}
	if c.remaining != 0 {
		return result, fmt.Errorf("tool batch has unfinished results")
	}
	for _, stored := range append([]*storage.Message{c.head}, c.results...) {
		message, err := MessageFromStorage(*stored)
		if err != nil {
			return result, err
		}
		c.Committed = append(c.Committed, message)
	}
	return result, nil
}

// NewToolBatch retains one assistant call array and ordered call/result links.
func NewToolBatch(head *storage.Message, calls []llm.ToolCallRequest, results []*storage.Message) (*storage.ToolBatch, error) {
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
		result.CreatedAt = head.CreatedAt
	}
	fields, err := storage.DecodeSessionMetadata(head.Metadata)
	if err != nil {
		return nil, err
	}
	// Calls and IDs contain only strings; decoded metadata already contains valid JSON.
	_ = fields.Set("tool_calls", calls)
	_ = fields.Set(storage.ToolResultMessageKey, ids)
	head.Metadata, _ = fields.Encode()
	return &storage.ToolBatch{Call: head, Results: results}, nil
}
