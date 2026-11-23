package responses

import (
	agentshare "elbot/internal/agent/share"

	"context"
	"encoding/json"
	"fmt"

	"elbot/internal/llm"
	"elbot/internal/storage"
)

type inputCommitter struct{ state *turnState }

func (c inputCommitter) Commit(ctx context.Context, commit storage.DialogueCommit) error {
	inputs := make([]storage.NativeInput, 0, len(commit.Messages))
	for _, message := range commit.Messages {
		if message.Role == storage.RoleAssistant && len(message.ToolCalls) == 1 {
			call := message.ToolCalls[0]
			raw, err := json.Marshal(map[string]string{"type": "function_call", "name": call.Name, "arguments": call.Arguments, "call_id": call.ID})
			if err != nil {
				return err
			}
			inputs = append(inputs, storage.NativeInput{ID: storage.NewID(), SessionID: message.SessionID, MessageID: message.ID, CallID: call.ID, ItemJSON: string(raw), MediaJSON: encodeMaterials(nil)})
			continue
		}
		segments, err := agentshare.MessageSegmentsFromStorage(message.Segments)
		if err != nil {
			return err
		}
		if len(segments) == 0 {
			segments = llm.TextSegments(message.Content)
		}
		input, err := queuedInput(message.SessionID, message.ID, "", message.ToolCallID, segments)
		if err != nil {
			return err
		}
		inputs = append(inputs, input)
	}
	commit.Native = &storage.NativeCommit{ExpectedCheckpointID: c.state.checkpointID(), Inputs: inputs}
	return c.state.commit(ctx, commit, "append_turn_input")
}

func (s *turnState) checkpointID() string {
	if s.checkpoint == nil {
		return ""
	}
	return s.checkpoint.ID
}

func (s *turnState) commit(ctx context.Context, commit storage.DialogueCommit, operation string) error {
	err := s.route.Messages.Commit(ctx, commit, operation)
	if err != nil {
		return err
	}
	if commit.Native != nil && commit.Native.Checkpoint.ID != "" {
		next := commit.Native.Checkpoint
		s.checkpoint = &next
		s.window = nil
		if commit.Native.ConsumeSeedID != "" && s.seed != nil {
			s.seed.Consumed = true
		}
	}
	return nil
}

func (s *turnState) advance(messageID string) storage.NativeCommit {
	parent := s.checkpointID()
	native := storage.NativeCommit{
		ExpectedCheckpointID: parent,
		Checkpoint:           storage.NativeCheckpoint{ID: storage.NewID(), SessionID: s.Session.ID, ParentID: parent, ExchangeID: s.exchange.ID, ResponseID: s.response.ID, MessageID: messageID},
		ConsumedInputs:       append([]string(nil), s.consumed...),
	}
	if s.seed != nil {
		native.Checkpoint.SeedID = s.seed.ID
		if !s.seed.Consumed {
			native.ConsumeSeedID = s.seed.ID
		}
	}
	return native
}

type replyCommitter struct{ state *turnState }

func (c replyCommitter) Commit(ctx context.Context, commit storage.DialogueCommit) error {
	id := ""
	if len(commit.Messages) != 0 {
		id = commit.Messages[0].ID
	}
	native := c.state.advance(id)
	commit.Native = &native
	return c.state.commit(ctx, commit, "append_assistant_message")
}

type toolCommitter struct {
	state     *turnState
	calls     []storage.NativeCall
	head      *storage.Message
	results   []*storage.Message
	inputs    []storage.NativeInput
	remaining int
}

func (c *toolCommitter) Begin(ctx context.Context, head *storage.Message) (err error) {

	s := c.state
	c.head = head
	c.inputs = make([]storage.NativeInput, len(s.calls))
	c.remaining = len(s.calls)
	native := s.advance("")
	batch, err := agentshare.NewToolBatch(ctx, s.route.Shared.NewMessageID, head, s.calls)
	if err != nil {
		return err
	}
	c.results = batch.Results
	for i, call := range s.calls {
		input, err := queuedInput(s.Session.ID, c.results[i].ID, s.exchange.ID, call.ID, llm.TextSegments(c.results[i].Content))
		if err != nil {
			return err
		}
		c.inputs[i] = input
		c.calls = append(c.calls, storage.NativeCall{ExchangeID: s.exchange.ID, CallID: call.ID, Name: call.Name, Arguments: call.Arguments, Ordinal: i, Status: "pending", ResultInputID: input.ID})
	}
	for i := range c.inputs {
		c.inputs[i].CreatedAt = head.CreatedAt
	}
	native.Inputs = c.inputs
	native.Checkpoint.MessageID = head.ID
	native.Calls = c.calls
	err = s.commit(ctx, storage.DialogueCommit{SessionID: s.Session.ID, ToolBatch: batch, Native: &native}, "begin_tool_transcript")
	if err != nil {
		return err
	}
	s.consumed = nil
	return nil
}
func (c *toolCommitter) Prepared(ctx context.Context, index int, call llm.ToolCallRequest) error {
	if index < 0 || index >= len(c.calls) || call.ID != c.calls[index].CallID || call.Name != c.calls[index].Name || call.Arguments != c.calls[index].Arguments {
		err := fmt.Errorf("native tool calls are read-only")
		return err
	}
	return nil
}
func (c *toolCommitter) Result(ctx context.Context, index int, _ llm.ToolCallRequest, message llm.LLMMessage, update *storage.ToolResultCommit) (err error) {

	s := c.state
	stored := update.Result
	stored.ID, stored.CreatedAt = c.results[index].ID, c.results[index].CreatedAt
	archived, err := s.route.Preparer.ArchiveMaterials(ctx, []llm.LLMMessage{message})
	if err != nil {
		return err
	}
	segments := archived[0].Segments
	stored.Content, stored.Segments = agentshare.MessageContent(segments), agentshare.StoredMessageSegments(segments)
	input, err := queuedInput(s.Session.ID, stored.ID, s.exchange.ID, c.calls[index].CallID, segments)
	if err != nil {
		return err
	}
	call := c.calls[index]
	call.Status = "completed"
	input.ID, input.CreatedAt = c.inputs[index].ID, c.inputs[index].CreatedAt
	c.calls[index], c.results[index], c.inputs[index] = call, stored, input
	stored.ToolResultStatus = "completed"
	native := storage.NativeCommit{ExpectedCheckpointID: s.checkpointID(), Inputs: []storage.NativeInput{input}, Calls: []storage.NativeCall{call}}
	if c.remaining == 1 {
		c.head.ToolBatchStatus = "completed"
		snapshot := *s.checkpoint
		snapshot.ID, snapshot.MessageID, snapshot.CreatedAt = storage.NewID(), c.head.ID, c.head.CreatedAt
		native.Snapshot = &snapshot
	}
	update.Call = c.head
	err = s.commit(context.WithoutCancel(ctx), storage.DialogueCommit{SessionID: s.Session.ID, ToolResult: update, Native: &native}, "save_tool_result")
	if err == nil {
		c.remaining--
	}
	return err
}

func (c *toolCommitter) Flush(context.Context) error {
	return agentshare.AppendToolTranscript(&c.state.projection, c.head, c.results, c.remaining)
}

// Close interrupted local tool rounds without executing historical calls.
func (s *turnState) closeInterruptedCalls(ctx context.Context, messages []storage.Message) error {
	if s.checkpoint == nil {
		return nil
	}
	calls, err := s.route.Repository.Calls(ctx, s.checkpoint.ExchangeID)
	if err != nil {
		return err
	}
	var pending []storage.NativeCall
	for _, call := range calls {
		if call.Status == "pending" {
			call.Status = "interrupted"
			pending = append(pending, call)
		}
	}
	if len(pending) == 0 {
		return nil
	}
	for i := range messages {
		head := &messages[i]
		if head.ID != s.checkpoint.MessageID {
			continue
		}
		head.ToolBatchStatus = "interrupted"
		snapshot := *s.checkpoint
		snapshot.ID, snapshot.CreatedAt = storage.NewID(), storage.Now()
		native := storage.NativeCommit{ExpectedCheckpointID: s.checkpointID(), Calls: pending, Snapshot: &snapshot}
		err = s.commit(ctx, storage.DialogueCommit{SessionID: s.Session.ID, ToolResult: &storage.ToolResultCommit{Call: head}, Native: &native}, "close_interrupted_native_tool")
		return err
	}
	return fmt.Errorf("interrupted tool batch header is missing")
}
