package agent

import (
	"context"
	"fmt"

	"elbot/internal/contextmgr"
	"elbot/internal/hook"
	"elbot/internal/llm"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/storage"
)

// preparedTurn contains material loaded before registering the cancellable request.
type preparedTurn struct {
	userMessage              *storage.Message
	userSegments             []llm.MessageSegment
	loaded                   *contextmgr.LoadedContext
	messages                 []storage.Message
	compactSeedOnCurrentUser bool
	summaryOnCurrentUser     bool
}

func (r *chatRunner) prepareTurn(ctx context.Context, session *storage.Session, text string) (*preparedTurn, error) {
	userSegments := materializeMedia(ctx, r.media, inboundSegments(ctx, text))
	userContent := llm.SegmentsContentText(userSegments)

	userMessage := &storage.Message{
		ID:                       storage.NewID(),
		SessionID:                session.ID,
		Role:                     storage.RoleUser,
		Content:                  userContent,
		Segments:                 storedMessageSegments(userSegments),
		ReplyToPlatformMessageID: inboundReplyMessageID(ctx),
	}
	if r.logger != nil {
		r.logger.Info("user input", "event", "user_message", "session_id", session.ID, "text", previewLogText(userContent))
	}

	loaded, err := r.contexts.Load(ctx, session.ID)
	if err != nil {
		return nil, err
	}
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
	messages := append([]storage.Message{}, loaded.Messages...)
	messages = append(messages, *userMessage)

	return &preparedTurn{
		userMessage: userMessage, userSegments: userSegments, loaded: loaded, messages: messages,
		compactSeedOnCurrentUser: compactSeedOnCurrentUser, summaryOnCurrentUser: summaryOnCurrentUser,
	}, nil
}

func (r *chatRunner) prepareMessages(s *chatTurnState, prepared *preparedTurn) error {
	userMessage, userSegments, loaded, messages := prepared.userMessage, prepared.userSegments, prepared.loaded, prepared.messages
	compactSeedOnCurrentUser, summaryOnCurrentUser := prepared.compactSeedOnCurrentUser, prepared.summaryOnCurrentUser
	s.output.PublishRuntimeStatus(s.ctx, runtimestatus.Snapshot{SessionID: s.session.ID, Phase: runtimestatus.PhasePreparing, Provider: s.selection.Provider, Model: s.selection.Model, Mode: s.session.Mode, TurnStartedAt: s.startedAt, StageStartedAt: s.startedAt})
	scope := r.identity.Scope(s.ctx)
	var err error
	s.messages, err = r.promptBuilder.Build(s.ctx, PromptBuildRequest{Session: s.session, Scope: scope, Messages: messages, Summary: loaded.Summary})
	if err != nil {
		return err
	}
	s.tools, err = r.toolsForSession(s.ctx, s.session)
	if err != nil {
		return err
	}
	turnEvent, err := r.hooks.Run(s.ctx, hook.Event{
		Point:   hook.PointLLMTurnPrepared,
		Session: hook.SessionContext{ID: s.session.ID},
		Message: hook.MessagePayload{ID: userMessage.ID, Role: string(llm.RoleUser), PlatformText: inboundTurnInput(s.ctx, s.text).PlatformText, Segments: append([]llm.MessageSegment(nil), userSegments...)},
		LLM: hook.LLMPayload{
			Provider: s.selection.Provider,
			Model:    s.selection.Model,
			Messages: llm.CloneMessages(s.messages),
			Tools:    s.tools,
		},
	})
	if err != nil {
		return fmt.Errorf("llm turn hook: %w", err)
	}
	if s.session.Mode == storage.SessionModeWork || s.session.Mode == storage.SessionModeBackground {
		s.tools = turnEvent.LLM.Tools
	}
	s.output.PublishRuntimeStatus(s.ctx, runtimestatus.Snapshot{SessionID: s.session.ID, Phase: runtimestatus.PhasePreparing, Provider: s.selection.Provider, Model: s.selection.Model, Mode: s.session.Mode, TurnStartedAt: s.startedAt, StageStartedAt: s.startedAt})
	canonicalUserSegments := materializeMedia(s.ctx, r.media, turnEvent.Message.Segments)
	promptUserSegments := canonicalUserSegments
	if compactSeedOnCurrentUser || summaryOnCurrentUser {
		promptUserSegments = llm.PrependSegmentText(promptUserSegments, summaryUserPrefix(loaded.Summary.Summary))
	}
	s.messages = llm.SetLatestUserSegments(s.messages, promptUserSegments)
	if compactSeedOnCurrentUser {
		userMessage.Content = llm.SegmentsContentText(promptUserSegments)
		userMessage.Segments = storedMessageSegments(promptUserSegments)
	} else {
		userMessage.Content = llm.SegmentsContentText(canonicalUserSegments)
		userMessage.Segments = storedMessageSegments(canonicalUserSegments)
	}
	if err := persistTurnMessage(s.ctx, r.messages, r.media, r.auditLogger, userMessage, "append_user_message"); err != nil {
		return err
	}
	if compactSeedOnCurrentUser {
		r.consumeContextCompactSeed(s.ctx, s.session)
	}

	return nil
}

func hasStorageUserMessage(messages []storage.Message) bool {
	for _, message := range messages {
		if message.Role == storage.RoleUser {
			return true
		}
	}
	return false
}
