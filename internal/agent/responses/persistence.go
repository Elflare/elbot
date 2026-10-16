package responses

import (
	"context"
	"fmt"

	"elbot/internal/agent/dialogue"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/storage"
)

type inputCommitter struct{ state *turnState }

func (c inputCommitter) Commit(ctx context.Context, message *storage.Message, mappings []storage.PlatformMessageMap) (events.LogResult, error) {
	if message == nil {
		err := fmt.Errorf("native input message is required")
		return events.LogResult{ResultStatus: events.ResultSkipped}, err
	}
	segments, err := dialogue.MessageSegmentsFromStorage(message.Segments)
	if err != nil {
		return events.LogResult{ResultStatus: events.ResultSkipped}, err
	}
	if len(segments) == 0 {
		segments = llm.TextSegments(message.Content)
	}
	input, err := queuedInput(message.SessionID, message.ID, "", "", segments)
	if err != nil {
		return events.LogResult{ResultStatus: events.ResultSkipped}, err
	}
	return c.state.commit(ctx, storage.DialogueCommit{
		SessionID:           message.SessionID,
		Messages:            []*storage.Message{message},
		PlatformMessageMaps: mappings,
		Native:              &storage.NativeCommit{ExpectedCheckpointID: c.state.checkpointID(), Inputs: []storage.NativeInput{input}},
	}, "append_user_message")
}

func (s *turnState) checkpointID() string {
	if s.checkpoint == nil {
		return ""
	}
	return s.checkpoint.ID
}

func (s *turnState) commit(ctx context.Context, commit storage.DialogueCommit, operation string) (events.LogResult, error) {
	logResult, err := s.route.Messages.Commit(ctx, commit, operation)
	if err != nil {
		return logResult, err
	}
	if commit.Native != nil && commit.Native.Checkpoint.ID != "" {
		next := commit.Native.Checkpoint
		s.checkpoint = &next
		s.window = nil
		if commit.Native.ConsumeSeedID != "" && s.seed != nil {
			s.seed.Consumed = true
		}
	}
	return logResult, nil
}

func (s *turnState) advance(messageID string) storage.NativeCommit {
	parent := s.checkpointID()
	native := storage.NativeCommit{
		ExpectedCheckpointID: parent,
		Checkpoint:           storage.NativeCheckpoint{ID: storage.NewID(), SessionID: s.session.ID, ParentID: parent, ExchangeID: s.exchange.ID, ResponseID: s.response.ID, MessageID: messageID},
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

func (c replyCommitter) Commit(ctx context.Context, message *storage.Message, mappings []storage.PlatformMessageMap) (events.LogResult, error) {
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
	return s.commit(ctx, storage.DialogueCommit{SessionID: s.session.ID, Messages: rows, Native: &native, PlatformMessageMaps: mappings}, "append_assistant_message")
}

type toolCommitter struct {
	state   *turnState
	calls   []storage.NativeCall
	head    *storage.Message
	results []*storage.Message
	inputs  []storage.NativeInput
}

func (c *toolCommitter) Begin(ctx context.Context, head *storage.Message) (events.LogResult, error) {
	s := c.state
	c.head = head
	c.results = make([]*storage.Message, len(s.calls))
	c.inputs = make([]storage.NativeInput, len(s.calls))
	native := s.advance("")
	for i, call := range s.calls {
		c.calls = append(c.calls, storage.NativeCall{ExchangeID: s.exchange.ID, CallID: call.ID, Name: call.Name, Arguments: call.Arguments, Ordinal: i, Status: "pending"})
	}
	native.Calls = append([]storage.NativeCall(nil), c.calls...)
	logResult, err := s.commit(ctx, storage.DialogueCommit{SessionID: s.session.ID, Native: &native}, "append_tool_transcript")
	if err != nil {
		return logResult, err
	}
	s.consumed = nil
	return logResult, nil
}
func (c *toolCommitter) Prepared(ctx context.Context, index int, call llm.ToolCallRequest) (events.LogResult, error) {
	if index < 0 || index >= len(c.calls) || call.ID != c.calls[index].CallID || call.Name != c.calls[index].Name || call.Arguments != c.calls[index].Arguments {
		err := fmt.Errorf("native tool calls are read-only")
		return events.LogResult{ResultStatus: events.ResultSkipped}, err
	}
	return events.LogResult{ResultStatus: events.ResultSkipped}, nil
}
func (c *toolCommitter) Started(ctx context.Context, index int, _ llm.ToolCallRequest) (events.LogResult, error) {
	call := c.calls[index]
	call.Status = "started"
	logResult, err := c.state.commit(ctx, storage.DialogueCommit{SessionID: c.state.session.ID, Native: &storage.NativeCommit{ExpectedCheckpointID: c.state.checkpointID(), Calls: []storage.NativeCall{call}}}, "start_native_tool")
	if err != nil {
		return logResult, err
	}
	c.calls[index] = call
	return logResult, nil
}
func (c *toolCommitter) Result(ctx context.Context, index int, request llm.ToolCallRequest, message llm.LLMMessage, stored *storage.Message) (events.LogResult, error) {
	s := c.state
	logResult, err := c.Prepared(ctx, index, request)
	if err != nil {
		return logResult, err
	}
	if stored.ID == "" {
		stored.ID = storage.NewID()
	}
	archived, err := s.route.Preparer.ArchiveMaterials(ctx, []llm.LLMMessage{message})
	if err != nil {
		return events.LogResult{ResultStatus: events.ResultSkipped}, err
	}
	segments := archived[0].Segments
	stored.Content, stored.Segments = dialogue.MessageContent(segments), dialogue.StoredMessageSegments(segments)
	input, err := queuedInput(s.session.ID, stored.ID, s.exchange.ID, c.calls[index].CallID, segments)
	if err != nil {
		return events.LogResult{ResultStatus: events.ResultSkipped}, err
	}
	call := c.calls[index]
	call.Status = "completed"
	call.ResultInputID = input.ID
	c.calls[index], c.results[index], c.inputs[index] = call, stored, input
	return events.LogResult{ResultStatus: events.ResultSkipped}, nil
}

func (c *toolCommitter) Flush(ctx context.Context) (events.LogResult, error) {
	s := c.state
	batch, err := dialogue.NewToolBatch(c.head, s.calls, c.results)
	if err != nil {
		return events.LogResult{ResultStatus: events.ResultSkipped}, err
	}
	snapshot := *s.checkpoint
	snapshot.ID, snapshot.MessageID = storage.NewID(), batch.Call.ID
	snapshot.CreatedAt = batch.Call.CreatedAt
	native := storage.NativeCommit{ExpectedCheckpointID: s.checkpointID(), Inputs: c.inputs, Calls: c.calls, Snapshot: &snapshot}
	logResult, err := s.commit(ctx, storage.DialogueCommit{SessionID: s.session.ID, ToolBatch: batch, Native: &native}, "append_tool_transcript")
	if err == nil {
		for _, stored := range append([]*storage.Message{batch.Call}, batch.Results...) {
			projected, err := dialogue.MessageFromStorage(*stored)
			if err != nil {
				return logResult, err
			}
			s.projection = append(s.projection, projected)
		}
	}
	return logResult, err
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
		if _, err := s.commit(ctx, storage.DialogueCommit{SessionID: s.session.ID, Native: &native}, "close_interrupted_native_tool"); err != nil {
			return err
		}
	}
	return nil
}
