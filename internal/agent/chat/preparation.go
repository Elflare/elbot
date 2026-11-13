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
	hasUserHistory := hasStorageUserMessage(loaded.Messages)
	summaryOnCurrentUser := loaded.Summary != nil && !hasUserHistory

	return operationresult.Result[agentshare.PreparedLoop]{Status: operationresult.Success, Data: &preparedLoop{route: r, materials: materials, summaryOnCurrentUser: summaryOnCurrentUser}}, nil
}

func (p *preparedLoop) InputCommitter() agentshare.MessageCommitter {
	return p.route.Messages.Committer("append_user_message")
}

func (p *preparedLoop) PrepareInput(ctx, requestCtx context.Context, in agentshare.LoopInput, out agentshare.Output) (operationresult.Result[*storage.Message], error) {
	r := p.route
	s := &chatTurnState{
		ctx:        ctx,
		requestCtx: requestCtx,
		session:    in.Session,
		output:     out,
		selection:  in.Selection,
		requestID:  in.RequestID,
		startedAt:  in.StartedAt,
	}
	p.state = s
	ctx = s.requestCtx
	if err := ctx.Err(); err != nil {
		return operationresult.Result[*storage.Message]{}, err
	}
	userMessage, userSegments, loaded := p.materials.UserMessage, p.materials.Input.Segments, p.materials.Loaded
	messages := append(append([]storage.Message{}, loaded.Messages...), *userMessage)
	summaryOnCurrentUser := p.summaryOnCurrentUser
	r.Status.Record(s.ctx, runtimestatus.Snapshot{
		SessionID:      s.session.ID,
		Phase:          runtimestatus.PhasePreparing,
		Provider:       s.selection.Provider,
		Model:          s.selection.Model,
		Mode:           s.session.Mode,
		TurnStartedAt:  s.startedAt,
		StageStartedAt: s.startedAt,
	})
	scope := r.Identity.Scope(ctx)
	var err error
	s.messages, err = r.PromptBuilder.Build(ctx, PromptBuildRequest{Session: s.session, Scope: scope, Messages: messages, Summary: loaded.Summary})
	if err != nil {
		return operationresult.Result[*storage.Message]{}, err
	}
	if err := ctx.Err(); err != nil {
		return operationresult.Result[*storage.Message]{}, err
	}
	s.tools, err = r.Tools.Schemas(ctx, s.session)
	if err != nil {
		return operationresult.Result[*storage.Message]{}, err
	}
	if err := ctx.Err(); err != nil {
		return operationresult.Result[*storage.Message]{}, err
	}
	canonicalUserSegments, tools, err := r.Preparer.PrepareInput(ctx, in, userMessage, p.materials.Input.PlatformText, userSegments, s.messages, s.tools, p.materials.Input.NeedsElyphRules)
	if err != nil {
		return operationresult.Result[*storage.Message]{}, err
	}
	s.tools = tools
	r.Status.Record(s.ctx, runtimestatus.Snapshot{
		SessionID:      s.session.ID,
		Phase:          runtimestatus.PhasePreparing,
		Provider:       s.selection.Provider,
		Model:          s.selection.Model,
		Mode:           s.session.Mode,
		TurnStartedAt:  s.startedAt,
		StageStartedAt: s.startedAt,
	})
	promptUserSegments := canonicalUserSegments
	if summaryOnCurrentUser {
		promptUserSegments = llm.PrependSegmentText(promptUserSegments, summaryUserPrefix(loaded.Summary.Summary))
	}
	s.messages = llm.SetLatestUserSegments(s.messages, promptUserSegments)
	userMessage.Content = agentshare.MessageContent(canonicalUserSegments)
	userMessage.Segments = agentshare.StoredMessageSegments(canonicalUserSegments)
	if err := ctx.Err(); err != nil {
		return operationresult.Result[*storage.Message]{}, err
	}
	return operationresult.Result[*storage.Message]{Status: operationresult.Success, Data: userMessage}, nil
}

func hasStorageUserMessage(messages []storage.Message) bool {
	for _, message := range messages {
		if message.Role == storage.RoleUser {
			return true
		}
	}
	return false
}
