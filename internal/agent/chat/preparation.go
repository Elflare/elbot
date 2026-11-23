package chat

import (
	agentshare "elbot/internal/agent/share"

	"context"
	"elbot/internal/operationresult"

	"elbot/internal/llm"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/storage"
)

func (r *Loop) PrepareTurn(ctx context.Context, materials agentshare.TurnMaterials) (operationresult.Result[agentshare.PreparedLoop], error) {
	loaded := materials.Loaded
	summaryOnCurrentUser := loaded.Summary != nil && !hasStorageUserMessage(loaded.Messages)

	return operationresult.Result[agentshare.PreparedLoop]{Status: operationresult.Success, Data: &preparedLoop{route: r, materials: materials, summaryOnCurrentUser: summaryOnCurrentUser}}, nil
}

func (p *preparedLoop) InputCommitter() agentshare.MessageCommitter {
	return p.route.Messages.Committer("append_turn_input")
}

func (p *preparedLoop) PrepareInput(ctx, requestCtx context.Context, in agentshare.LoopInput, out agentshare.Output) (operationresult.Result[storage.DialogueCommit], error) {
	r := p.route
	s := &chatTurnState{LoopState: agentshare.NewLoopState(ctx, requestCtx, in), output: out}
	p.state = s
	ctx = s.RequestCtx
	if err := ctx.Err(); err != nil {
		return operationresult.Result[storage.DialogueCommit]{}, err
	}
	userMessage, userSegments, loaded := p.materials.UserMessage, p.materials.Input.Segments, p.materials.Loaded
	messages := append([]storage.Message{}, loaded.Messages...)
	for _, m := range p.materials.Input.Reports {
		messages = append(messages, *m)
	}
	if userMessage != nil {
		messages = append(messages, *userMessage)
	}
	snapshot := runtimestatus.Snapshot{
		SessionID:      s.Session.ID,
		Phase:          runtimestatus.PhasePreparing,
		Provider:       s.Selection.Provider,
		Model:          s.Selection.Model,
		Mode:           s.Session.Mode,
		TurnStartedAt:  s.StartedAt,
		StageStartedAt: s.StartedAt,
	}
	r.Status.Record(s.Ctx, snapshot)
	scope := r.Identity.Scope(ctx)
	var err error
	s.messages, err = r.PromptBuilder.Build(ctx, PromptBuildRequest{Session: s.Session, Scope: scope, Messages: messages, Summary: loaded.Summary})
	if err != nil {
		return operationresult.Result[storage.DialogueCommit]{}, err
	}
	if err := ctx.Err(); err != nil {
		return operationresult.Result[storage.DialogueCommit]{}, err
	}
	s.tools, err = r.Shared.Tools.Schemas(ctx, s.Session)
	if err != nil {
		return operationresult.Result[storage.DialogueCommit]{}, err
	}
	if err := ctx.Err(); err != nil {
		return operationresult.Result[storage.DialogueCommit]{}, err
	}
	if userMessage == nil {
		return operationresult.Result[storage.DialogueCommit]{Status: operationresult.Success, Data: p.materials.InputCommit(nil, false)}, nil
	}
	canonicalUserSegments, tools, elyphRulesShown, err := r.Preparer.PrepareInput(ctx, in, userMessage, p.materials.Input.PlatformText, userSegments, s.messages, s.tools, p.materials.Input.NeedsElyphRules)
	if err != nil {
		return operationresult.Result[storage.DialogueCommit]{}, err
	}
	s.tools = tools
	r.Status.Record(s.Ctx, snapshot)
	promptUserSegments := canonicalUserSegments
	if p.summaryOnCurrentUser {
		promptUserSegments = llm.PrependSegmentText(promptUserSegments, summaryUserPrefix(loaded.Summary.Summary))
	}
	s.messages = llm.SetLatestUserSegments(s.messages, promptUserSegments)
	if err := ctx.Err(); err != nil {
		return operationresult.Result[storage.DialogueCommit]{}, err
	}
	return operationresult.Result[storage.DialogueCommit]{Status: operationresult.Success, Data: p.materials.InputCommit(canonicalUserSegments, elyphRulesShown)}, nil
}

func hasStorageUserMessage(messages []storage.Message) bool {
	for _, message := range messages {
		if message.Role == storage.RoleUser {
			return true
		}
	}
	return false
}
