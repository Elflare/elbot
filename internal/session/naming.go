package session

import (
	"context"
	"encoding/json"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/storage"
)

const maxNamingFailures = 3

type namingState struct {
	inFlight bool
	done     bool
	failures int
}

func (s *Service) MaybeScheduleNaming(ctx context.Context, sessionID string) {
	if s.titleGen == nil {
		return
	}
	workerCtx, finish, ok := s.beginNaming()
	if !ok {
		return
	}
	ctx, cancelPreparation := context.WithCancel(ctx)
	stopPreparation := context.AfterFunc(workerCtx, cancelPreparation)
	defer func() { stopPreparation(); cancelPreparation() }()
	scheduled := false
	defer func() {
		if !scheduled {
			finish()
		}
	}()
	if ctx.Err() != nil || s.titleRenamed(ctx, sessionID) {
		return
	}
	messages, err := s.store.Messages().ListBySession(ctx, sessionID)
	if err != nil {
		if ctx.Err() == nil && workerCtx.Err() == nil {
			s.notifyNamingFailed(ctx, NamingFailedEvent{SessionID: sessionID, Reason: "load messages", Err: err, TriggeredAt: storage.Now()})
		}
		return
	}
	conversationMessages := filterConversationMessages(messages)
	if len(conversationMessages) < s.namingConfig.TriggerStep {
		return
	}
	namingMessages := append([]storage.Message(nil), conversationMessages[:s.namingConfig.TriggerStep]...)
	if workerCtx.Err() != nil || !s.markNamingInFlight(sessionID) {
		return
	}

	s.notifyNamingScheduled(ctx, NamingScheduledEvent{SessionID: sessionID, TriggeredAt: storage.Now(), MessageCount: len(namingMessages), TriggerStep: s.namingConfig.TriggerStep})
	scheduled = true
	go func() {
		defer finish()
		defer func() {
			if workerCtx.Err() == nil {
				return
			}
			s.mu.Lock()
			state := s.namingStates[sessionID]
			state.inFlight = false
			s.namingStates[sessionID] = state
			s.mu.Unlock()
		}()
		s.generateTitle(workerCtx, sessionID, namingMessages)
	}()
}

func (s *Service) titleRenamed(ctx context.Context, sessionID string) bool {
	session, err := s.store.Sessions().Get(ctx, sessionID)
	if err != nil || strings.TrimSpace(session.Metadata) == "" {
		return false
	}
	var metadata struct {
		TitleRenamed bool `json:"title_renamed"`
	}
	if err := json.Unmarshal([]byte(session.Metadata), &metadata); err != nil {
		return false
	}
	return metadata.TitleRenamed
}

func (s *Service) generateTitle(ctx context.Context, sessionID string, messages []storage.Message) {
	if ctx.Err() != nil {
		return
	}
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	facts.SessionID = sessionID
	ctx = contextinfo.WithExecution(ctx, facts)
	session, err := s.store.Sessions().Get(ctx, sessionID)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		s.markNamingFailed(sessionID)
		s.notifyNamingFailed(ctx, NamingFailedEvent{SessionID: sessionID, Reason: "load session", Err: err, TriggeredAt: storage.Now(), MessageCount: len(messages)})
		return
	}

	result, err := s.titleGen.GenerateTitle(ctx, messages)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		s.handleNamingFailure(ctx, session, messages, "generate title", err, "llm_error", result, "")
		return
	}
	title := normalizeTitle(result.RawTitle)
	if title == "" || isPlaceholderTitle(title) {
		s.handleNamingFailure(ctx, session, messages, "invalid title", nil, "invalid_response", result, title)
		return
	}

	_, applied, err := s.saveGeneratedTitle(ctx, sessionID, title, false)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		s.handleNamingFailure(ctx, session, messages, "update title", err, "storage_update", result, title)
		return
	}
	s.markNamingDone(sessionID)
	if !applied {
		return
	}
	s.notifyNamingCompleted(ctx, NamingCompletedEvent{SessionID: sessionID, Title: title, TriggeredAt: storage.Now(), MessageCount: len(messages), Provider: result.Provider, Model: result.Model})
}

func (s *Service) markNamingInFlight(sessionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.namingStates[sessionID]
	if state.inFlight || state.done || state.failures >= maxNamingFailures {
		return false
	}
	state.inFlight = true
	s.namingStates[sessionID] = state
	return true
}

func (s *Service) markNamingDone(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.namingStates[sessionID]
	state.inFlight = false
	state.done = true
	s.namingStates[sessionID] = state
}

func (s *Service) markNamingFailed(sessionID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.namingStates[sessionID]
	state.inFlight = false
	state.failures++
	s.namingStates[sessionID] = state
	return state.failures
}

func (s *Service) handleNamingFailure(ctx context.Context, session *storage.Session, messages []storage.Message, reason string, err error, stage string, result TitleResult, normalizedTitle string) {
	if ctx.Err() != nil {
		return
	}
	failures := s.markNamingFailed(session.ID)
	event := NamingFailedEvent{
		SessionID: session.ID, Title: session.Title, Stage: stage, LLMCall: llmCallStatus(err),
		GeneratedTitleRaw: result.RawTitle, GeneratedTitleNormalized: normalizedTitle, Provider: result.Provider, Model: result.Model,
		InvalidReason: invalidTitleReason(normalizedTitle), Reason: reason, Err: err,
		TriggeredAt: storage.Now(), MessageCount: len(messages), FailureCount: failures, MaxFailures: maxNamingFailures,
	}
	fallback := fallbackTitle(messages)
	if fallback != "" {
		latest, applied, updateErr := s.saveGeneratedTitle(ctx, session.ID, fallback, failures < maxNamingFailures)
		if updateErr != nil {
			event.FallbackErr = updateErr
		} else {
			event.Title = latest.Title
			if applied {
				event.FallbackTitle, event.FallbackApplied = fallback, true
				s.markNamingDone(session.ID)
			}
		}
	}
	if ctx.Err() == nil {
		s.notifyNamingFailed(ctx, event)
	}
}

func (s *Service) notifyNamingScheduled(ctx context.Context, event NamingScheduledEvent) {
	_ = s.namingSignals.Scheduled.Emit(ctx, event)
}

func (s *Service) notifyNamingCompleted(ctx context.Context, event NamingCompletedEvent) {
	_ = s.namingSignals.Completed.Emit(ctx, event)
}

func (s *Service) notifyNamingFailed(ctx context.Context, event NamingFailedEvent) {
	_ = s.namingSignals.Failed.Emit(ctx, event)
}

func filterConversationMessages(messages []storage.Message) []storage.Message {
	out := make([]storage.Message, 0, len(messages))
	for _, message := range messages {
		if message.Role != storage.RoleUser && message.Role != storage.RoleAssistant {
			continue
		}
		if strings.TrimSpace(message.Content) != "" {
			out = append(out, message)
		}
	}
	return out
}

func normalizeTitle(text string) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\n", " "))
	text = strings.Trim(text, "\"'` ")
	return defaultTitle(text)
}

func isPlaceholderTitle(title string) bool {
	return strings.EqualFold(strings.TrimSpace(title), "New session")
}

func llmCallStatus(err error) string {
	if err != nil {
		return "failed"
	}
	return "succeeded"
}

func invalidTitleReason(title string) string {
	if strings.TrimSpace(title) == "" {
		return "empty title"
	}
	if isPlaceholderTitle(title) {
		return "placeholder title"
	}
	return ""
}

func fallbackTitle(messages []storage.Message) string {
	for _, message := range messages {
		if message.Role == storage.RoleUser {
			return defaultTitle(message.Content)
		}
	}
	return ""
}

// The manual-title flag is checked in the same transaction as both normal and
// fallback title writes, after any slow model request has completed.
func (s *Service) saveGeneratedTitle(ctx context.Context, id, title string, onlyPlaceholder bool) (*storage.Session, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	applied := false
	row, err := s.store.Sessions().Mutate(ctx, id, func(row *storage.Session) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		fields, err := storage.DecodeSessionMetadata(row.Metadata)
		if err != nil {
			return err
		}
		var renamed bool
		if raw, ok := fields["title_renamed"]; ok {
			if err := json.Unmarshal(raw, &renamed); err != nil {
				return err
			}
		}
		if renamed || (onlyPlaceholder && !isPlaceholderTitle(row.Title)) {
			return nil
		}
		row.Title, row.UpdatedAt = title, storage.Now()
		applied = true
		return nil
	})
	return row, applied, err
}
