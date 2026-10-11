package session

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
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
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	facts.SessionID = sessionID
	workerCtx = contextinfo.WithExecution(workerCtx, facts)
	event := NamingCompletedEvent{Identity: events.LogIdentity{SessionID: &facts.SessionID}, TriggeredAt: storage.Now(), MaxFailures: maxNamingFailures}
	if facts.RunID != "" {
		event.Identity.RunID = &facts.RunID
	}
	if facts.Attempt != "" {
		event.Identity.Attempt = &facts.Attempt
	}
	if facts.RequestID != "" {
		event.Identity.RequestID = &facts.RequestID
	}
	if facts.RootRequestID != "" {
		event.Identity.RootRequestID = &facts.RootRequestID
	}
	event.PreparationOperation = "load messages"
	messages, err := s.store.Messages().ListBySession(ctx, sessionID)
	if err != nil {
		event.PreparationError = err
		event.PreparationResultStatus = events.ResultFailed
		if errors.Is(err, context.Canceled) {
			event.PreparationResultStatus = events.ResultCanceled
		}
		event.ResultStatus = event.PreparationResultStatus
		event.CompletedAt = storage.Now()
		s.notifyNamingCompleted(ctx, event)
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

	event.PreparationResultStatus = events.ResultSucceeded
	event.MessageCount = new(int)
	*event.MessageCount = len(namingMessages)
	event.Scheduled = true
	s.notifyNamingScheduled(ctx, NamingScheduledEvent{Identity: event.Identity, TriggeredAt: event.TriggeredAt, MessageCount: len(namingMessages), TriggerStep: s.namingConfig.TriggerStep})
	scheduled = true
	go func() {
		defer finish()
		s.generateTitle(workerCtx, sessionID, namingMessages, event)
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

func (s *Service) generateTitle(ctx context.Context, sessionID string, messages []storage.Message, event NamingCompletedEvent) {
	defer func() {
		s.mu.Lock()
		state := s.namingStates[sessionID]
		state.inFlight = false
		s.namingStates[sessionID] = state
		event.FailureCount = state.failures
		s.mu.Unlock()
		event.CompletedAt = storage.Now()
		s.notifyNamingCompleted(ctx, event)
	}()
	if ctx.Err() != nil {
		event.CancellationError = ctx.Err()
		event.ResultStatus = events.ResultFailed
		if errors.Is(ctx.Err(), context.Canceled) {
			event.ResultStatus = events.ResultCanceled
		}
		return
	}
	event.PreparationOperation = "load session"
	session, err := s.store.Sessions().Get(ctx, sessionID)
	if err != nil {
		event.PreparationError = err
		event.PreparationResultStatus = events.ResultFailed
		if errors.Is(err, context.Canceled) {
			event.PreparationResultStatus = events.ResultCanceled
		}
		event.ResultStatus = event.PreparationResultStatus
		if ctx.Err() == nil {
			event.FailureCount = s.markNamingFailed(sessionID)
		}
		return
	}
	event.PreparationResultStatus = events.ResultSucceeded
	event.Title = &session.Title
	if ctx.Err() != nil {
		event.CancellationError = ctx.Err()
		event.ResultStatus = events.ResultFailed
		if errors.Is(ctx.Err(), context.Canceled) {
			event.ResultStatus = events.ResultCanceled
		}
		return
	}
	result, err := s.titleGen.GenerateTitle(ctx, messages)
	event.Provider, event.Model, event.GeneratedTitleRaw = result.Provider, result.Model, result.RawTitle
	if err != nil {
		event.GenerationError = err
		event.GenerationResultStatus = events.ResultFailed
		if errors.Is(err, context.Canceled) {
			event.GenerationResultStatus = events.ResultCanceled
		}
		event.ResultStatus = event.GenerationResultStatus
		if event.ResultStatus == events.ResultFailed {
			s.handleNamingFailure(ctx, session, messages, &event)
		}
		return
	}
	event.GenerationResultStatus = events.ResultSucceeded
	if ctx.Err() != nil {
		event.CancellationError = ctx.Err()
		event.ResultStatus = events.ResultFailed
		if errors.Is(ctx.Err(), context.Canceled) {
			event.ResultStatus = events.ResultCanceled
		}
		return
	}
	title := normalizeTitle(result.RawTitle)
	event.GeneratedTitleNormalized = title
	event.TitleValid = new(bool)
	event.InvalidReason = invalidTitleReason(title)
	*event.TitleValid = event.InvalidReason == ""
	if title == "" || isPlaceholderTitle(title) {
		event.ResultStatus = events.ResultFailed
		s.handleNamingFailure(ctx, session, messages, &event)
		return
	}
	latest, applied, err := s.saveGeneratedTitle(ctx, sessionID, title, false)
	if err != nil {
		event.PersistenceError = err
		event.PersistenceResultStatus = events.ResultFailed
		if errors.Is(err, context.Canceled) {
			event.PersistenceResultStatus = events.ResultCanceled
		}
		event.ResultStatus = event.PersistenceResultStatus
		if event.ResultStatus == events.ResultFailed {
			s.handleNamingFailure(ctx, session, messages, &event)
		}
		return
	}
	event.Title = &latest.Title
	s.markNamingDone(sessionID)
	if !applied {
		event.PersistenceResultStatus = events.ResultSkipped
		event.ResultStatus = events.ResultSkipped
		return
	}
	event.PersistenceResultStatus = events.ResultSucceeded
	event.ResultStatus = events.ResultSucceeded
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
	state.failures++
	s.namingStates[sessionID] = state
	return state.failures
}

func (s *Service) handleNamingFailure(ctx context.Context, session *storage.Session, messages []storage.Message, event *NamingCompletedEvent) {
	if ctx.Err() != nil {
		event.CancellationError = ctx.Err()
		return
	}
	failures := s.markNamingFailed(session.ID)
	event.FailureCount = failures
	fallback := fallbackTitle(messages)
	if fallback != "" {
		event.FallbackTitle = fallback
		latest, applied, updateErr := s.saveGeneratedTitle(ctx, session.ID, fallback, failures < maxNamingFailures)
		if updateErr != nil {
			event.FallbackError = updateErr
			event.FallbackResultStatus = events.ResultFailed
			if errors.Is(updateErr, context.Canceled) {
				event.FallbackResultStatus = events.ResultCanceled
			}
			event.ResultStatus = events.ResultFailed
		} else {
			event.Title = &latest.Title
			event.FallbackResultStatus = events.ResultSkipped
			if applied {
				event.FallbackResultStatus = events.ResultSucceeded
				// A generation failure may recover; a failed main persistence remains an independent failure.
				if event.PersistenceResultStatus != events.ResultFailed {
					event.ResultStatus = events.ResultSucceeded
				}
				s.markNamingDone(session.ID)
			}
		}
	}
}

func (s *Service) notifyNamingScheduled(ctx context.Context, event NamingScheduledEvent) {
	for _, value := range []**string{&event.Identity.SessionID, &event.Identity.RunID, &event.Identity.Attempt, &event.Identity.RequestID, &event.Identity.RootRequestID} {
		if *value != nil {
			copy := **value
			*value = &copy
		}
	}
	_, _ = s.namingSignals.Scheduled.Emit(ctx, event)
}

func (s *Service) notifyNamingCompleted(ctx context.Context, event NamingCompletedEvent) {
	for _, value := range []**string{&event.Identity.SessionID, &event.Identity.RunID, &event.Identity.Attempt, &event.Identity.RequestID, &event.Identity.RootRequestID, &event.Title} {
		if *value != nil {
			copy := **value
			*value = &copy
		}
	}
	if event.MessageCount != nil {
		copy := *event.MessageCount
		event.MessageCount = &copy
	}
	if event.TitleValid != nil {
		copy := *event.TitleValid
		event.TitleValid = &copy
	}
	_, _ = s.namingSignals.Completed.Emit(context.WithoutCancel(ctx), event)
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
