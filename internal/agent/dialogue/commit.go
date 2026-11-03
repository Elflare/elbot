package dialogue

import (
	agentshare "elbot/internal/agent/share"

	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"fmt"
	"log/slog"
	"slices"

	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

// CommitGate uses the original activation and live attempt, not public IDs as
// authorization. It is held only for local persistence, never across external IO.
type CommitGate struct {
	Sessions interface {
		EnterBinding(context.Context, *session.Binding) (context.Context, func(), error)
		EnterSessions(context.Context, ...string) (context.Context, func(), error)
	}
	Turns interface {
		Snapshot(string) turn.Snapshot
	}
	View ExecutionView
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
		current := g.Turns.Snapshot(sessionID)
		if current.Execution != execution || current.Attempt != turn.AttemptFromContext(ctx) {
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

func (s *MessageStore) Committer(operation string) agentshare.MessageCommitter {
	return messageCommitter{store: s, operation: operation}
}

func (c messageCommitter) Commit(ctx context.Context, message *storage.Message, mappings []storage.PlatformMessageMap) error {
	if message == nil {
		return nil
	}
	return c.store.Commit(ctx, storage.DialogueCommit{SessionID: message.SessionID, Messages: []*storage.Message{message}, PlatformMessageMaps: mappings}, c.operation)
}

// ToolTranscriptCommitter reserves ordered slots and commits each final result.
type ToolTranscriptCommitter struct {
	Messages  *MessageStore
	SessionID string
	committed []llm.LLMMessage
	head      *storage.Message
	calls     []llm.ToolCallRequest
	results   []*storage.Message
	remaining int
}

func (c *ToolTranscriptCommitter) Begin(ctx context.Context, head *storage.Message) (err error) {
	step := "prepare_tool_transcript"
	defer func() {
		// The commit boundary owns failures after preparation is complete.
		if err == nil || step == "commit" {
			return
		}
		facts, _ := contextinfo.ExecutionFromContext(ctx)
		facts.SessionID = head.SessionID
		record := events.LogRecord{Category: events.LogRuntime, Level: slog.LevelError, Module: "agent", EventName: "tool_batch_prepare_failed", ResultStatus: events.LogStatus(err), Content: fmt.Sprintf("operation=%s error=%v", step, err)}
		if record.ResultStatus == events.ResultCanceled {
			record.Level = slog.LevelInfo
		}
		agentshare.EmitAgentLog(ctx, facts, record)
	}()

	c.SessionID, c.head = head.SessionID, head
	metadata, err := agentshare.AssistantMessageMetadata(head.Metadata)
	if err != nil {
		return err
	}
	c.calls = append([]llm.ToolCallRequest(nil), metadata.ToolCalls...)
	c.results = make([]*storage.Message, len(c.calls))
	c.remaining = len(c.calls)
	for i, call := range c.calls {
		stored := agentshare.ToolResultStorageMessage(c.SessionID, llm.LLMMessage{Role: llm.RoleTool, Name: call.Name, ToolCallID: call.ID, Segments: llm.TextSegments("tool outcome is unknown: result has not been saved; do not assume success or retry automatically")})
		fields, _ := storage.DecodeSessionMetadata(stored.Metadata)
		_ = fields.Set("tool_result_status", "pending")
		stored.Metadata, _ = fields.Encode()
		c.results[i] = &stored
	}
	batch, err := agentshare.NewToolBatch(ctx, c.Messages.NewMessageID, head, c.calls, c.results)
	if err != nil {
		return err
	}
	fields, _ := storage.DecodeSessionMetadata(head.Metadata)
	_ = fields.Set("tool_batch_status", "pending")
	head.Metadata, _ = fields.Encode()
	step = "commit"
	return c.Messages.Commit(ctx, storage.DialogueCommit{SessionID: c.SessionID, ToolBatch: batch}, "begin_tool_transcript")
}
func (c *ToolTranscriptCommitter) Prepared(context.Context, int, llm.ToolCallRequest) error {
	return nil
}
func (c *ToolTranscriptCommitter) Result(ctx context.Context, index int, call llm.ToolCallRequest, _ llm.LLMMessage, update *storage.ToolResultCommit) error {
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
	err := c.Messages.Commit(context.WithoutCancel(ctx), commit, "save_tool_result")
	if err == nil {
		c.remaining--
	}
	return err
}
func (c *ToolTranscriptCommitter) Flush(ctx context.Context) (err error) {
	defer func() {
		if err == nil {
			return
		}
		facts, _ := contextinfo.ExecutionFromContext(ctx)
		facts.SessionID = c.SessionID
		record := events.LogRecord{Category: events.LogRuntime, Level: slog.LevelError, Module: "agent", EventName: "tool_transcript_projection_failed", ResultStatus: events.LogStatus(err), Content: err.Error()}
		if record.ResultStatus == events.ResultCanceled {
			record.Level = slog.LevelInfo
		}
		agentshare.EmitAgentLog(ctx, facts, record)
	}()

	if c.remaining != 0 {
		return fmt.Errorf("tool batch has unfinished results")
	}
	for _, stored := range append([]*storage.Message{c.head}, c.results...) {
		message, err := agentshare.MessageFromStorage(*stored)
		if err != nil {
			return err
		}
		c.committed = append(c.committed, message)
	}
	return nil
}

func (c *ToolTranscriptCommitter) Committed() []llm.LLMMessage { return c.committed }
