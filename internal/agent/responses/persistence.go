package responses

import (
	agentshare "elbot/internal/agent/share"

	"context"
	"fmt"

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
	segments, err := agentshare.MessageSegmentsFromStorage(message.Segments)
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
	state     *turnState
	calls     []storage.NativeCall
	head      *storage.Message
	results   []*storage.Message
	inputs    []storage.NativeInput
	remaining int
}

func (c *toolCommitter) Begin(ctx context.Context, head *storage.Message) (events.LogResult, error) {
	s := c.state
	c.head = head
	c.results = make([]*storage.Message, len(s.calls))
	c.inputs = make([]storage.NativeInput, len(s.calls))
	c.remaining = len(s.calls)
	native := s.advance("")
	for i, call := range s.calls {
		message := llm.LLMMessage{Role: llm.RoleTool, Name: call.Name, ToolCallID: call.ID, Segments: llm.TextSegments("tool outcome is unknown: result has not been saved; do not assume success or retry automatically")}
		stored := agentshare.ToolResultStorageMessage(s.session.ID, message)
		stored.ID = storage.NewID()
		fields, _ := storage.DecodeSessionMetadata(stored.Metadata)
		_ = fields.Set("tool_result_status", "pending")
		stored.Metadata, _ = fields.Encode()
		c.results[i] = &stored
		input, err := queuedInput(s.session.ID, stored.ID, s.exchange.ID, call.ID, message.Segments)
		if err != nil {
			return events.LogResult{ResultStatus: events.ResultSkipped}, err
		}
		c.inputs[i] = input
		c.calls = append(c.calls, storage.NativeCall{ExchangeID: s.exchange.ID, CallID: call.ID, Name: call.Name, Arguments: call.Arguments, Ordinal: i, Status: "pending", ResultInputID: input.ID})
	}
	batch, err := agentshare.NewToolBatch(head, s.calls, c.results)
	if err != nil {
		return events.LogResult{ResultStatus: events.ResultSkipped}, err
	}
	fields, _ := storage.DecodeSessionMetadata(head.Metadata)
	_ = fields.Set("tool_batch_status", "pending")
	head.Metadata, _ = fields.Encode()
	for i := range c.inputs {
		c.inputs[i].CreatedAt = head.CreatedAt
	}
	native.Inputs = c.inputs
	native.Checkpoint.MessageID = head.ID
	native.Calls = append([]storage.NativeCall(nil), c.calls...)
	logResult, err := s.commit(ctx, storage.DialogueCommit{SessionID: s.session.ID, ToolBatch: batch, Native: &native}, "begin_tool_transcript")
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
func (c *toolCommitter) Result(ctx context.Context, index int, _ llm.ToolCallRequest, message llm.LLMMessage, update *storage.ToolResultCommit) (events.LogResult, error) {
	s := c.state
	stored := update.Result
	stored.ID, stored.CreatedAt = c.results[index].ID, c.results[index].CreatedAt
	archived, err := s.route.Preparer.ArchiveMaterials(ctx, []llm.LLMMessage{message})
	if err != nil {
		return events.LogResult{ResultStatus: events.ResultSkipped}, err
	}
	segments := archived[0].Segments
	stored.Content, stored.Segments = agentshare.MessageContent(segments), agentshare.StoredMessageSegments(segments)
	input, err := queuedInput(s.session.ID, stored.ID, s.exchange.ID, c.calls[index].CallID, segments)
	if err != nil {
		return events.LogResult{ResultStatus: events.ResultSkipped}, err
	}
	call := c.calls[index]
	call.Status = "completed"
	input.ID, input.CreatedAt = c.inputs[index].ID, c.inputs[index].CreatedAt
	c.calls[index], c.results[index], c.inputs[index] = call, stored, input
	fields, _ := storage.DecodeSessionMetadata(stored.Metadata)
	_ = fields.Set("tool_result_status", "completed")
	stored.Metadata, _ = fields.Encode()
	native := storage.NativeCommit{ExpectedCheckpointID: s.checkpointID(), Inputs: []storage.NativeInput{input}, Calls: []storage.NativeCall{call}}
	if c.remaining == 1 {
		fields, _ := storage.DecodeSessionMetadata(c.head.Metadata)
		_ = fields.Set("tool_batch_status", "completed")
		c.head.Metadata, _ = fields.Encode()
		snapshot := *s.checkpoint
		snapshot.ID, snapshot.MessageID, snapshot.CreatedAt = storage.NewID(), c.head.ID, c.head.CreatedAt
		native.Snapshot = &snapshot
	}
	update.Call = c.head
	logResult, err := s.commit(context.WithoutCancel(ctx), storage.DialogueCommit{SessionID: s.session.ID, ToolResult: update, Native: &native}, "save_tool_result")
	if err == nil {
		c.remaining--
	}
	return logResult, err
}

func (c *toolCommitter) Flush(ctx context.Context) (events.LogResult, error) {
	s := c.state
	logResult := events.LogResult{ResultStatus: events.ResultSkipped}
	if c.remaining != 0 {
		return logResult, fmt.Errorf("tool batch has unfinished results")
	}
	for _, stored := range append([]*storage.Message{c.head}, c.results...) {
		projected, err := agentshare.MessageFromStorage(*stored)
		if err != nil {
			return logResult, err
		}
		s.projection = append(s.projection, projected)
	}
	return logResult, nil
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
		fields, err := storage.DecodeSessionMetadata(head.Metadata)
		if err != nil {
			return err
		}
		_ = fields.Set("tool_batch_status", "interrupted")
		head.Metadata, _ = fields.Encode()
		snapshot := *s.checkpoint
		snapshot.ID, snapshot.CreatedAt = storage.NewID(), storage.Now()
		native := storage.NativeCommit{ExpectedCheckpointID: s.checkpointID(), Calls: pending, Snapshot: &snapshot}
		_, err = s.commit(ctx, storage.DialogueCommit{SessionID: s.session.ID, ToolResult: &storage.ToolResultCommit{Call: head}, Native: &native}, "close_interrupted_native_tool")
		return err
	}
	return fmt.Errorf("interrupted tool batch header is missing")
}
