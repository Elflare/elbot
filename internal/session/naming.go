package session

import (
	"context"
	"encoding/json"
	"strings"

	"elbot/internal/storage"
)

const maxNamingFailures = 3

type namingState struct {
	inFlight bool
	done     bool
	failures int
}

type noopNamingNotifier struct{}

func (noopNamingNotifier) NotifyNamingScheduled(context.Context, NamingScheduledEvent) {}
func (noopNamingNotifier) NotifyNamingCompleted(context.Context, NamingCompletedEvent) {}
func (noopNamingNotifier) NotifyNamingFailed(context.Context, NamingFailedEvent)       {}

func (s *Service) MaybeScheduleNaming(ctx context.Context, sessionID string) {
	if s.titleRenamed(ctx, sessionID) || s.titleGen == nil {
		return
	}
	messages, err := s.store.Messages().ListBySession(ctx, sessionID)
	if err != nil {
		s.notifyNamingFailed(ctx, NamingFailedEvent{SessionID: sessionID, Reason: "load messages", Err: err, TriggeredAt: storage.Now()})
		return
	}
	conversationMessages := filterConversationMessages(messages)
	if len(conversationMessages) < s.namingConfig.TriggerStep {
		return
	}
	namingMessages := append([]storage.Message(nil), conversationMessages[:s.namingConfig.TriggerStep]...)
	if !s.markNamingInFlight(sessionID) {
		return
	}

	s.notifyNamingScheduled(ctx, NamingScheduledEvent{SessionID: sessionID, TriggeredAt: storage.Now(), MessageCount: len(namingMessages), TriggerStep: s.namingConfig.TriggerStep})
	go s.generateTitle(context.Background(), sessionID, namingMessages)
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
	session, err := s.store.Sessions().Get(ctx, sessionID)
	if err != nil {
		s.markNamingFailed(sessionID)
		s.notifyNamingFailed(ctx, NamingFailedEvent{SessionID: sessionID, Reason: "load session", Err: err, TriggeredAt: storage.Now(), MessageCount: len(messages)})
		return
	}

	result, err := s.titleGen.GenerateTitle(ctx, messages)
	if err != nil {
		s.handleNamingFailure(ctx, session, messages, "generate title", err, "llm_error", "", "")
		return
	}
	title := normalizeTitle(result.RawTitle)
	if title == "" || isPlaceholderTitle(title) {
		s.handleNamingFailure(ctx, session, messages, "invalid title", nil, "invalid_response", result.RawTitle, title)
		return
	}

	session.Title = title
	session.UpdatedAt = storage.Now()
	if err := s.store.Sessions().Update(ctx, session); err != nil {
		s.handleNamingFailure(ctx, session, messages, "update title", err, "storage_update", result.RawTitle, title)
		return
	}
	s.markNamingDone(sessionID)
	s.notifyNamingCompleted(ctx, NamingCompletedEvent{SessionID: sessionID, Title: title, TriggeredAt: storage.Now(), MessageCount: len(messages)})
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

func (s *Service) handleNamingFailure(ctx context.Context, session *storage.Session, messages []storage.Message, reason string, err error, stage, rawTitle, normalizedTitle string) {
	failures := s.markNamingFailed(session.ID)
	event := NamingFailedEvent{
		SessionID:                session.ID,
		Title:                    session.Title,
		Stage:                    stage,
		LLMCall:                  llmCallStatus(err),
		GeneratedTitleRaw:        rawTitle,
		GeneratedTitleNormalized: normalizedTitle,
		InvalidReason:            invalidTitleReason(normalizedTitle),
		Reason:                   reason,
		Err:                      err,
		TriggeredAt:              storage.Now(),
		MessageCount:             len(messages),
		FailureCount:             failures,
		MaxFailures:              maxNamingFailures,
	}
	shouldFallback := failures >= maxNamingFailures || isPlaceholderTitle(session.Title)
	if shouldFallback {
		event.FallbackTitle = fallbackTitle(messages)
		event.FallbackApplied = event.FallbackTitle != ""
		s.markNamingDone(session.ID)
	}
	s.notifyNamingFailed(ctx, event)
	if !shouldFallback {
		return
	}

	if event.FallbackTitle != "" {
		session.Title = event.FallbackTitle
		session.UpdatedAt = storage.Now()
		if updateErr := s.store.Sessions().Update(ctx, session); updateErr != nil {
			s.notifyNamingFailed(ctx, NamingFailedEvent{SessionID: session.ID, Title: session.Title, Reason: "fallback title", Err: updateErr, TriggeredAt: storage.Now(), MessageCount: len(messages)})
		}
	}
}

func (s *Service) notifyNamingScheduled(ctx context.Context, event NamingScheduledEvent) {
	if s.notifier != nil {
		s.notifier.NotifyNamingScheduled(ctx, event)
	}
}

func (s *Service) notifyNamingCompleted(ctx context.Context, event NamingCompletedEvent) {
	if s.notifier != nil {
		s.notifier.NotifyNamingCompleted(ctx, event)
	}
}

func (s *Service) notifyNamingFailed(ctx context.Context, event NamingFailedEvent) {
	if s.notifier != nil {
		s.notifier.NotifyNamingFailed(ctx, event)
	}
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
