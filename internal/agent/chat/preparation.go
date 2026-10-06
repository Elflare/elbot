package chat

import (
	"context"

	"elbot/internal/agent/dialogue"
	"elbot/internal/contextmgr"
	"elbot/internal/llm"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/storage"
)

func (r *Loop) PrepareTurn(ctx context.Context, materials dialogue.TurnMaterials) (dialogue.PreparedLoop, error) {
	session, loaded := materials.Session, materials.Loaded
	hasUserHistory := hasStorageUserMessage(loaded.Messages)
	compactSeedOnCurrentUser := false
	seed, err := contextmgr.PendingCompact(session)
	if err != nil {
		return nil, err
	}
	if seed != nil {
		if !hasUserHistory {
			loaded.Summary = &storage.ContextSummary{Summary: seed.Summary}
			compactSeedOnCurrentUser = true
		} else {
			r.consumeContextCompactSeed(ctx, session)
		}
	}
	summaryOnCurrentUser := loaded.Summary != nil && !hasUserHistory && !compactSeedOnCurrentUser

	return &preparedLoop{route: r, materials: materials, compactSeedOnCurrentUser: compactSeedOnCurrentUser, summaryOnCurrentUser: summaryOnCurrentUser}, nil
}

func (p *preparedLoop) PrepareInput(ctx, requestCtx context.Context, in dialogue.LoopInput, out dialogue.Output) (*storage.Message, error) {
	r := p.route
	s := &chatTurnState{ctx: ctx, requestCtx: requestCtx, session: in.Session, text: in.Text, output: out, selection: in.Selection, requestID: in.RequestID, startedAt: in.StartedAt}
	p.state = s
	ctx = s.requestCtx
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	userMessage, userSegments, loaded := p.materials.UserMessage, p.materials.Input.Segments, p.materials.Loaded
	messages := append(append([]storage.Message{}, loaded.Messages...), *userMessage)
	compactSeedOnCurrentUser, summaryOnCurrentUser := p.compactSeedOnCurrentUser, p.summaryOnCurrentUser
	s.output.PublishRuntimeStatus(s.ctx, runtimestatus.Snapshot{SessionID: s.session.ID, Phase: runtimestatus.PhasePreparing, Provider: s.selection.Provider, Model: s.selection.Model, Mode: s.session.Mode, TurnStartedAt: s.startedAt, StageStartedAt: s.startedAt})
	scope := r.Preparer.Identity.Scope(ctx)
	var err error
	s.messages, err = r.PromptBuilder.Build(ctx, PromptBuildRequest{Session: s.session, Scope: scope, Messages: messages, Summary: loaded.Summary})
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.tools, err = r.Tools.Schemas(ctx, s.session)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	canonicalUserSegments, tools, err := r.Preparer.PrepareInput(ctx, in, userMessage, p.materials.Input.PlatformText, userSegments, s.messages, s.tools)
	if err != nil {
		return nil, err
	}
	s.tools = tools
	s.output.PublishRuntimeStatus(s.ctx, runtimestatus.Snapshot{SessionID: s.session.ID, Phase: runtimestatus.PhasePreparing, Provider: s.selection.Provider, Model: s.selection.Model, Mode: s.session.Mode, TurnStartedAt: s.startedAt, StageStartedAt: s.startedAt})
	promptUserSegments := canonicalUserSegments
	if compactSeedOnCurrentUser || summaryOnCurrentUser {
		promptUserSegments = llm.PrependSegmentText(promptUserSegments, summaryUserPrefix(loaded.Summary.Summary))
	}
	s.messages = llm.SetLatestUserSegments(s.messages, promptUserSegments)
	if compactSeedOnCurrentUser {
		userMessage.Content = llm.SegmentsContentText(promptUserSegments)
		userMessage.Segments = dialogue.StoredMessageSegments(promptUserSegments)
	} else {
		userMessage.Content = llm.SegmentsContentText(canonicalUserSegments)
		userMessage.Segments = dialogue.StoredMessageSegments(canonicalUserSegments)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return userMessage, nil
}

func hasStorageUserMessage(messages []storage.Message) bool {
	for _, message := range messages {
		if message.Role == storage.RoleUser {
			return true
		}
	}
	return false
}
