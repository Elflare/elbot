package share

import (
	"context"

	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/operationresult"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/turn"
)

// LoopDeps is wired once for both protocol loops.
type LoopDeps struct {
	NewMessageID     func(context.Context) (string, error) // Allocate a history ID before a protocol attaches native material.
	SelectionForTurn func(context.Context, *storage.Session) modelmgr.Selection
	View             interface {
		Context(context.Context) context.Context
		RefreshSession(context.Context, *storage.Session) (context.Context, error)
		WithModel(context.Context, modelmgr.Selection) context.Context
		CheckSelection(*storage.Session, modelmgr.Selection) (operationresult.Result[struct{}], error)
	}
	Turns interface {
		StartToolPhase(string, string) bool
		DrainMergedInput(string, string) turn.Input
	}
	Tools interface {
		Schemas(context.Context, *storage.Session) ([]llm.ToolSchema, error)
		MaxRoundsPerTurn() int
		ExecuteWithCommitter(context.Context, *storage.Session, []llm.ToolCallRequest, string, string, string, Output, tool.ToolCommitter) tool.RunResult
	}
}

// LoopState lives for one prepared turn. Protocols own their request materials.
type LoopState struct {
	LoopInput
	Ctx, RequestCtx    context.Context // Turn/output context and separately cancellable request context.
	foregroundPrepared bool            // Apply the new selection and request materials once, not once per tool round.
	rounds             int             // Executed tool batches; skipped calls do not count.
}

func NewLoopState(ctx, requestCtx context.Context, in LoopInput) LoopState {
	return LoopState{LoopInput: in, Ctx: ctx, RequestCtx: requestCtx, foregroundPrepared: in.Session.Mode != storage.SessionModeBackground}
}

func (s *LoopState) RefreshSession(d LoopDeps, check func(modelmgr.Selection) operationresult.Result[struct{}], refresh func() error) (operationresult.Result[struct{}], error) {
	var err error
	s.RequestCtx, err = d.View.RefreshSession(s.RequestCtx, s.Session)
	if err != nil {
		return operationresult.Result[struct{}]{}, err
	}
	s.Ctx = d.View.Context(s.Ctx)
	if !s.foregroundPrepared && s.Session.Mode != storage.SessionModeBackground {
		s.Selection = d.SelectionForTurn(s.RequestCtx, s.Session)
		if checked := check(s.Selection); checked.Status != operationresult.Success {
			return checked, nil
		}
		s.RequestCtx = d.View.WithModel(s.RequestCtx, s.Selection)
		s.Ctx = d.View.WithModel(s.Ctx, s.Selection)
		if err := refresh(); err != nil {
			return operationresult.Result[struct{}]{}, err
		}
		s.foregroundPrepared = true
	}
	return d.View.CheckSelection(s.Session, s.Selection)
}

func (s *LoopState) StartCall(d LoopDeps, out Output) (*PendingInput, delivery.MessageStream) {
	return DrainPending(d.Turns, s.Session.ID, turn.AttemptFromContext(s.Ctx)), out.StartStream(s.RequestCtx)
}

func (s *LoopState) LLMSnapshot(usage *llm.Usage) runtimestatus.Snapshot {
	return runtimestatus.Snapshot{
		SessionID: s.Session.ID, Phase: runtimestatus.PhaseLLM, Provider: s.Selection.Provider, Model: s.Selection.Model,
		Mode: s.Session.Mode, RequestID: s.RequestID, TurnStartedAt: s.StartedAt, StageStartedAt: storage.Now(), Usage: usage,
	}
}

// SendToolOutput finishes the intermediate text, then sends the route's remaining extras.
func (s *LoopState) SendToolOutput(out Output, stream delivery.MessageStream, text string, extras []delivery.Output) error {
	if _, err := stream.Send(s.RequestCtx, delivery.StreamEvent{Type: delivery.StreamSend, Text: text}); err != nil {
		return err
	}
	_, err := out.Send(s.Ctx, delivery.StreamEvent{Type: delivery.StreamSendExtras, Outputs: extras})
	return err
}

// RunTools enters the tool phase before running the first batch.
// The protocol committer owns the atomic write, including any native checkpoint.
func (s *LoopState) RunTools(d LoopDeps, out Output, calls []llm.ToolCallRequest, text, reasoning string, committer tool.ToolCommitter, skippedText func(int) string) (tool.RunResult, bool) {
	maxRounds := d.Tools.MaxRoundsPerTurn()
	if s.rounds == 0 && !d.Turns.StartToolPhase(s.Session.ID, turn.AttemptFromContext(s.Ctx)) {
		return tool.RunResult{Stopped: true}, false
	}
	if s.rounds >= maxRounds {
		skipText := skippedText(maxRounds)
		head := ToolCallStorageMessage(s.Session.ID, text, text, calls, reasoning)
		if err := committer.Begin(s.RequestCtx, &head); err != nil {
			return tool.RunResult{Err: err}, true
		}
		for i, call := range calls {
			message := llm.LLMMessage{Role: llm.RoleTool, Name: call.Name, ToolCallID: call.ID, Segments: llm.TextSegments(skipText)}
			row := ToolResultStorageMessage(s.Session.ID, message)
			if err := committer.Result(s.RequestCtx, i, call, message, &storage.ToolResultCommit{Result: &row}); err != nil {
				return tool.RunResult{Err: err}, true
			}
		}
		return tool.RunResult{Err: committer.Flush(s.RequestCtx)}, true
	}
	s.rounds++
	r := d.Tools.ExecuteWithCommitter(s.RequestCtx, s.Session, calls, text, text, reasoning, out, committer)
	if r.Stopped && r.Err == nil {
		r.Err = s.RequestCtx.Err()
	}
	return r, false
}

func (s *LoopState) AppendUserText(d LoopDeps, persistence MessageCommitter, messages *[]llm.LLMMessage, text string) error {
	row := &storage.Message{SessionID: s.Session.ID, Role: storage.RoleUser, Content: text}
	if err := storage.PrepareMessageID(s.RequestCtx, d.NewMessageID, row); err != nil {
		return err
	}
	if err := persistence.CommitDialogue(s.RequestCtx, storage.DialogueCommit{SessionID: s.Session.ID, Messages: []*storage.Message{row}}); err != nil {
		return err
	}
	projected, err := MessageFromStorage(*row)
	if err != nil {
		return err
	}
	*messages = append(*messages, projected)
	return nil
}

func ReplaceSystemMessages(messages, prompt []llm.LLMMessage) []llm.LLMMessage {
	updated := make([]llm.LLMMessage, 0, len(messages)+len(prompt))
	for _, m := range prompt {
		if m.Role == llm.RoleSystem {
			updated = append(updated, m)
		}
	}
	for _, m := range messages {
		if m.Role != llm.RoleSystem {
			updated = append(updated, m)
		}
	}
	return updated
}
