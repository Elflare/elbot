package dialogue

import (
	agentshare "elbot/internal/agent/share"
	"time"

	"context"
	"fmt"
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

// commitInput records the rule card in history and advances the live session only after persistence succeeds.
func commitInput(ctx context.Context, session *storage.Session, commit storage.DialogueCommit, persistence agentshare.MessageCommitter) error {
	if !commit.ElyphRulesShown {
		return persistence.CommitDialogue(ctx, commit)
	}
	i := slices.IndexFunc(commit.Messages, func(m *storage.Message) bool { return m.Role == llm.RoleUser })
	message := commit.Messages[i]
	fields, err := storage.DecodeSessionMetadata(message.Metadata)
	if err != nil {
		return err
	}
	_ = fields.Set("elyph_rules_shown", true)
	message.Metadata, err = fields.Encode()
	if err != nil {
		return err
	}
	fields, err = storage.DecodeSessionMetadata(session.Metadata)
	if err != nil {
		return err
	}
	_ = fields.Set("elyph_rules_shown", true)
	metadata, err := fields.Encode()
	if err != nil {
		return err
	}
	if err := persistence.CommitDialogue(ctx, commit); err != nil {
		return err
	}
	session.Metadata = metadata
	return nil
}

type messageCommitter struct {
	store     *MessageStore
	operation string
}

func (s *MessageStore) Committer(operation string) agentshare.MessageCommitter {
	return messageCommitter{store: s, operation: operation}
}

func (c messageCommitter) CommitDialogue(ctx context.Context, commit storage.DialogueCommit) error {
	if len(commit.Messages) == 0 {
		return nil
	}
	return c.store.Commit(ctx, commit, c.operation)
}

// ToolTranscriptCommitter reserves ordered slots and commits each final result.
type ToolTranscriptCommitter struct {
	Messages  *MessageStore
	committed []llm.LLMMessage
	head      *storage.Message
	results   []*storage.Message
	remaining int
}

func (c *ToolTranscriptCommitter) Begin(ctx context.Context, head *storage.Message) error {
	c.head = head
	c.remaining = len(head.ToolCalls)
	batch, err := agentshare.NewToolBatch(ctx, c.Messages.NewMessageID, head)
	if err != nil {
		return err
	}
	c.results = batch.Results
	return c.Messages.Commit(ctx, storage.DialogueCommit{SessionID: c.head.SessionID, ToolBatch: batch}, "begin_tool_transcript")
}
func (c *ToolTranscriptCommitter) Prepared(context.Context, int, llm.ToolCallRequest) error {
	return nil
}
func (c *ToolTranscriptCommitter) Result(ctx context.Context, index int, call llm.ToolCallRequest, _ llm.LLMMessage, update *storage.ToolResultCommit) error {
	commit := storage.DialogueCommit{SessionID: c.head.SessionID, ToolResult: update}
	if call.Arguments != c.head.ToolCalls[index].Arguments {
		previous := media.ArgumentMediaIDs(c.head.ToolCalls[index].Arguments)
		for _, id := range media.ArgumentMediaIDs(call.Arguments) {
			if !slices.Contains(previous, id) {
				commit.MediaReferences = append(commit.MediaReferences, storage.MediaReference{MediaID: id, OwnerType: "session_tool", OwnerID: c.head.SessionID, SessionID: c.head.SessionID, Purpose: "input", CreatedAt: time.Now()})
			}
		}
	}
	stored := update.Result
	stored.ID, stored.CreatedAt = c.results[index].ID, c.results[index].CreatedAt
	c.head.ToolCalls[index] = call
	c.results[index] = stored
	stored.ToolResultStatus = "completed"
	if c.remaining == 1 {
		c.head.ToolBatchStatus = "completed"
	}
	update.Call = c.head
	err := c.Messages.Commit(context.WithoutCancel(ctx), commit, "save_tool_result")
	if err == nil {
		c.remaining--
	}
	return err
}
func (c *ToolTranscriptCommitter) Flush(context.Context) error {
	return agentshare.AppendToolTranscript(&c.committed, c.head, c.results, c.remaining)
}

func (c *ToolTranscriptCommitter) Committed() []llm.LLMMessage { return c.committed }
