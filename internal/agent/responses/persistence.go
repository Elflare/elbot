package responses

import (
	"context"
	"fmt"

	"elbot/internal/agent/dialogue"
	"elbot/internal/llm"
	"elbot/internal/storage"
)

type inputCommitter struct{ state *turnState }

func (c inputCommitter) Commit(ctx context.Context, message *storage.Message) error {
	if message == nil {
		return fmt.Errorf("native input message is required")
	}
	segments := dialogue.MessageSegmentsFromStorage(message.Segments)
	if len(segments) == 0 {
		segments = llm.TextSegments(message.Content)
	}
	input, err := queuedInput(message.SessionID, message.ID, "", "", segments)
	if err != nil {
		return err
	}
	return c.state.commit(ctx, storage.DialogueCommit{SessionID: message.SessionID, Messages: []*storage.Message{message}, Native: &storage.NativeCommit{ExpectedCheckpointID: c.state.checkpointID(), Inputs: []storage.NativeInput{input}}}, "append_user_message")
}

func (s *turnState) checkpointID() string {
	if s.checkpoint == nil {
		return ""
	}
	return s.checkpoint.ID
}

func (s *turnState) commit(ctx context.Context, commit storage.DialogueCommit, operation string) error {
	if err := s.route.Messages.Commit(ctx, commit, operation); err != nil {
		return err
	}
	if commit.Native != nil && commit.Native.Checkpoint.ID != "" {
		next := commit.Native.Checkpoint
		s.checkpoint = &next
		if commit.Native.ConsumeSeedID != "" && s.seed != nil {
			s.seed.Consumed = true
		}
	}
	return nil
}

func (s *turnState) advance(messageID string) storage.NativeCommit {
	parent := s.checkpointID()
	native := storage.NativeCommit{ExpectedCheckpointID: parent, Checkpoint: storage.NativeCheckpoint{ID: storage.NewID(), SessionID: s.session.ID, ParentID: parent, ExchangeID: s.exchange.ID, ResponseID: s.response.ID, MessageID: messageID}, ConsumedInputs: append([]string(nil), s.consumed...)}
	if s.seed != nil {
		native.Checkpoint.SeedID = s.seed.ID
		if !s.seed.Consumed {
			native.ConsumeSeedID = s.seed.ID
		}
	}
	return native
}

type replyCommitter struct{ state *turnState }

func (c replyCommitter) Commit(ctx context.Context, message *storage.Message) error {
	s := c.state
	var rows []*storage.Message
	id := ""
	if message != nil {
		if message.ID == "" {
			message.ID = storage.NewID()
		}
		id = message.ID
		rows = []*storage.Message{message}
	}
	native := s.advance(id)
	return s.commit(ctx, storage.DialogueCommit{SessionID: s.session.ID, Messages: rows, Native: &native}, "append_assistant_message")
}

type toolCommitter struct {
	state *turnState
	calls []storage.NativeCall
}

func (c *toolCommitter) Begin(ctx context.Context, head *storage.Message) error {
	s := c.state
	text := dialogue.ToolBatchText(head)
	var rows []*storage.Message
	id := ""
	if text != nil {
		rows, id = []*storage.Message{text}, text.ID
	}
	native := s.advance(id)
	for i, call := range s.calls {
		c.calls = append(c.calls, storage.NativeCall{ExchangeID: s.exchange.ID, CallID: call.ID, Name: call.Name, Arguments: call.Arguments, Ordinal: i, Status: "pending"})
	}
	native.Calls = append([]storage.NativeCall(nil), c.calls...)
	if err := s.commit(ctx, storage.DialogueCommit{SessionID: s.session.ID, Messages: rows, Native: &native}, "append_tool_transcript"); err != nil {
		return err
	}
	s.consumed = nil
	return nil
}
func (c *toolCommitter) Prepared(ctx context.Context, index int, call llm.ToolCallRequest) error {
	if index < 0 || index >= len(c.calls) || call.ID != c.calls[index].CallID || call.Name != c.calls[index].Name || call.Arguments != c.calls[index].Arguments {
		return fmt.Errorf("native tool calls are read-only")
	}
	return nil
}
func (c *toolCommitter) Started(ctx context.Context, index int, _ llm.ToolCallRequest) error {
	call := c.calls[index]
	call.Status = "started"
	if err := c.state.commit(ctx, storage.DialogueCommit{SessionID: c.state.session.ID, Native: &storage.NativeCommit{ExpectedCheckpointID: c.state.checkpointID(), Calls: []storage.NativeCall{call}}}, "start_native_tool"); err != nil {
		return err
	}
	c.calls[index] = call
	return nil
}
func (c *toolCommitter) Result(ctx context.Context, index int, request llm.ToolCallRequest, message llm.LLMMessage, stored *storage.Message) error {
	s := c.state
	if err := c.Prepared(ctx, index, request); err != nil {
		return err
	}
	if stored.ID == "" {
		stored.ID = storage.NewID()
	}
	segments := message.Segments
	if s.route.Calls.Media != nil {
		segments = s.route.Calls.Media.Materialize(ctx, segments)
	}
	input, err := queuedInput(s.session.ID, stored.ID, s.exchange.ID, c.calls[index].CallID, segments)
	if err != nil {
		return err
	}
	call := c.calls[index]
	call.Status = "completed"
	call.ResultInputID = input.ID
	pair := dialogue.NewToolPair(s.session.ID, request, stored)
	snapshot := *s.checkpoint
	snapshot.ID, snapshot.MessageID = storage.NewID(), pair.Call.ID
	snapshot.CreatedAt = pair.Call.CreatedAt
	native := storage.NativeCommit{ExpectedCheckpointID: s.checkpointID(), Inputs: []storage.NativeInput{input}, Calls: []storage.NativeCall{call}, Snapshot: &snapshot}
	if err := s.commit(ctx, storage.DialogueCommit{SessionID: s.session.ID, ToolPair: pair, Native: &native}, "append_tool_transcript"); err != nil {
		return err
	}
	c.calls[index] = call
	return nil
}

// Close interrupted local tool rounds without executing historical calls.
func (s *turnState) closeInterruptedCalls(ctx context.Context) error {
	if s.checkpoint == nil {
		return nil
	}
	calls, err := s.route.Repository.Calls(ctx, s.checkpoint.ExchangeID)
	if err != nil {
		return err
	}
	for _, call := range calls {
		if call.ResultInputID != "" {
			continue
		}
		text := fmt.Sprintf("tool call %s was not executed: previous turn stopped", call.Name)
		if call.Status == "started" {
			text = fmt.Sprintf("tool call %s outcome is unknown: previous turn stopped before its result was saved; do not assume it succeeded", call.Name)
		}
		message := llm.LLMMessage{Role: llm.RoleTool, Name: call.Name, ToolCallID: call.CallID, Segments: llm.TextSegments(text)}
		input, err := queuedInput(s.session.ID, "", call.ExchangeID, call.CallID, message.Segments)
		if err != nil {
			return err
		}
		call.Status = "interrupted"
		call.ResultInputID = input.ID
		native := storage.NativeCommit{ExpectedCheckpointID: s.checkpointID(), Inputs: []storage.NativeInput{input}, Calls: []storage.NativeCall{call}}
		if err := s.commit(ctx, storage.DialogueCommit{SessionID: s.session.ID, Native: &native}, "close_interrupted_native_tool"); err != nil {
			return err
		}
	}
	return nil
}
