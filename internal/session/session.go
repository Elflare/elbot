package session

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"elbot/internal/storage"
)

type Scope struct {
	ActorID         string
	Platform        string
	PlatformScopeID string
	IsCLI           bool
}

type Status struct {
	Session           *storage.Session
	MessageCount      int
	LastUserPreview   string
	LastAnswerPreview string
}

type NamingConfig struct {
	TriggerStep int
}

type Config struct {
	NamingConfig
	DefaultMode string
}

type TitleResult struct {
	RawTitle string
}

type TitleGenerator interface {
	GenerateTitle(ctx context.Context, messages []storage.Message) (TitleResult, error)
}

type NamingScheduledEvent struct {
	SessionID    string
	TriggeredAt  time.Time
	MessageCount int
	TriggerStep  int
}

type NamingCompletedEvent struct {
	SessionID    string
	Title        string
	TriggeredAt  time.Time
	MessageCount int
}

type NamingFailedEvent struct {
	SessionID                string
	Title                    string
	Stage                    string
	LLMCall                  string
	GeneratedTitleRaw        string
	GeneratedTitleNormalized string
	InvalidReason            string
	Reason                   string
	Err                      error
	TriggeredAt              time.Time
	MessageCount             int
	FailureCount             int
	MaxFailures              int
	FallbackApplied          bool
	FallbackTitle            string
}

type NamingNotifier interface {
	NotifyNamingScheduled(ctx context.Context, event NamingScheduledEvent)
	NotifyNamingCompleted(ctx context.Context, event NamingCompletedEvent)
	NotifyNamingFailed(ctx context.Context, event NamingFailedEvent)
}

type noopNamingNotifier struct{}

func (noopNamingNotifier) NotifyNamingScheduled(context.Context, NamingScheduledEvent) {}
func (noopNamingNotifier) NotifyNamingCompleted(context.Context, NamingCompletedEvent) {}
func (noopNamingNotifier) NotifyNamingFailed(context.Context, NamingFailedEvent)       {}

type namingState struct {
	inFlight bool
	done     bool
	failures int
}

const maxNamingFailures = 3

type Service struct {
	store        storage.Store
	mu           sync.Mutex
	current      map[string]string
	namingConfig NamingConfig
	titleGen     TitleGenerator
	notifier     NamingNotifier
	namingStates map[string]namingState
	defaultMode  string
}

func NewService(store storage.Store) *Service {
	return NewServiceWithNaming(store, NamingConfig{TriggerStep: 1}, nil, nil)
}

func NewServiceWithNaming(store storage.Store, cfg NamingConfig, titleGen TitleGenerator, notifier NamingNotifier) *Service {
	return NewServiceWithConfig(store, Config{NamingConfig: cfg, DefaultMode: storage.SessionModeWork}, titleGen, notifier)
}

func NewServiceWithConfig(store storage.Store, cfg Config, titleGen TitleGenerator, notifier NamingNotifier) *Service {
	if cfg.TriggerStep <= 0 {
		cfg.TriggerStep = 1
	}
	if cfg.DefaultMode == "" {
		cfg.DefaultMode = storage.SessionModeWork
	}
	if err := validateMode(cfg.DefaultMode); err != nil {
		cfg.DefaultMode = storage.SessionModeWork
	}
	if notifier == nil {
		notifier = noopNamingNotifier{}
	}
	return &Service{
		store:        store,
		current:      map[string]string{},
		namingConfig: cfg.NamingConfig,
		titleGen:     titleGen,
		notifier:     notifier,
		namingStates: map[string]namingState{},
		defaultMode:  cfg.DefaultMode,
	}
}

func (s *Service) GetOrCreateCurrent(ctx context.Context, scope Scope, firstMessage string) (*storage.Session, error) {
	if current, err := s.Current(ctx, scope); err == nil {
		return current, nil
	}

	return s.CreateWithMode(ctx, scope, defaultTitle(firstMessage), s.defaultMode)
}

func (s *Service) Create(ctx context.Context, scope Scope, title string) (*storage.Session, error) {
	return s.CreateWithMode(ctx, scope, title, s.defaultMode)
}

func (s *Service) CreateWithMode(ctx context.Context, scope Scope, title, mode string) (*storage.Session, error) {
	if err := validateMode(mode); err != nil {
		return nil, err
	}
	if strings.TrimSpace(title) == "" {
		title = "New session"
	}
	session := &storage.Session{
		OwnerID:         scope.ActorID,
		Platform:        scope.Platform,
		PlatformScopeID: scope.PlatformScopeID,
		Mode:            mode,
		Status:          storage.SessionStatusActive,
		Title:           title,
	}
	if err := s.store.Sessions().Create(ctx, session); err != nil {
		return nil, err
	}
	s.setCurrent(scope, session.ID)
	return session, nil
}

func (s *Service) DefaultMode() string {
	if s.defaultMode == "" {
		return storage.SessionModeWork
	}
	return s.defaultMode
}

func (s *Service) SetMode(ctx context.Context, scope Scope, mode string) (*storage.Session, error) {
	if err := validateMode(mode); err != nil {
		return nil, err
	}
	session, err := s.Current(ctx, scope)
	if err != nil {
		return nil, err
	}
	if session.Mode == mode {
		return session, nil
	}
	session.Mode = mode
	session.UpdatedAt = storage.Now()
	if err := s.store.Sessions().Update(ctx, session); err != nil {
		return nil, err
	}
	return session, nil
}

func validateMode(mode string) error {
	if mode == storage.SessionModeWork || mode == storage.SessionModeChat {
		return nil
	}
	return fmt.Errorf("invalid session mode %q", mode)
}

func (s *Service) Resume(ctx context.Context, scope Scope, sessionID string) (*storage.Session, error) {
	session, err := s.store.Sessions().Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if !s.canAccess(scope, session) {
		return nil, fmt.Errorf("session %s is not in current platform scope", sessionID)
	}
	s.setCurrent(scope, session.ID)
	return session, nil
}

func (s *Service) Fork(ctx context.Context, scope Scope, fromMessageID string) (*storage.Session, error) {
	fromMessageID = strings.TrimSpace(fromMessageID)
	if fromMessageID == "" {
		return nil, fmt.Errorf("message id is required")
	}
	message, err := s.store.Messages().Get(ctx, fromMessageID)
	if err != nil {
		return nil, err
	}
	if message.Role != storage.RoleAssistant {
		return nil, fmt.Errorf("can only fork from assistant messages")
	}
	source, err := s.store.Sessions().Get(ctx, message.SessionID)
	if err != nil {
		return nil, err
	}
	if !s.canAccess(scope, source) {
		return nil, fmt.Errorf("session %s is not in current platform scope", source.ID)
	}

	fork := &storage.Session{
		ParentSessionID:   source.ID,
		ForkFromMessageID: message.ID,
		OwnerID:           scope.ActorID,
		Platform:          scope.Platform,
		PlatformScopeID:   scope.PlatformScopeID,
		Mode:              source.Mode,
		Status:            storage.SessionStatusActive,
		Title:             forkTitle(source.Title),
	}
	if err := s.store.Sessions().Create(ctx, fork); err != nil {
		return nil, err
	}
	s.setCurrent(scope, fork.ID)
	return fork, nil
}

func (s *Service) Current(ctx context.Context, scope Scope) (*storage.Session, error) {
	s.mu.Lock()
	id := s.current[s.scopeKey(scope)]
	s.mu.Unlock()
	if id == "" {
		return nil, storage.ErrNotFound
	}
	return s.store.Sessions().Get(ctx, id)
}

func (s *Service) List(ctx context.Context, scope Scope, query string, limit int) ([]storage.SessionSummary, error) {
	return s.ListPage(ctx, scope, query, limit, 0, false)
}

func (s *Service) ListPage(ctx context.Context, scope Scope, query string, limit, offset int, archivedOnly bool) ([]storage.SessionSummary, error) {
	return s.store.Sessions().List(ctx, storage.ListSessionsRequest{
		ActorID:                 scope.ActorID,
		Platform:                scope.Platform,
		PlatformScopeID:         scope.PlatformScopeID,
		IncludeAllPlatforms:     scope.IsCLI,
		IncludeSamePlatformCron: !scope.IsCLI,
		ArchivedOnly:            archivedOnly,
		Query:                   query,
		Limit:                   limit,
		Offset:                  offset,
	})
}

func (s *Service) Status(ctx context.Context, scope Scope) (*Status, error) {
	session, err := s.Current(ctx, scope)
	if err != nil {
		return nil, err
	}
	messages, err := s.store.Messages().ListBySession(ctx, session.ID)
	if err != nil {
		return nil, err
	}
	status := &Status{Session: session, MessageCount: len(messages)}
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		switch message.Role {
		case storage.RoleUser:
			if status.LastUserPreview == "" {
				status.LastUserPreview = preview(message.Content)
			}
		case storage.RoleAssistant:
			if status.LastAnswerPreview == "" {
				status.LastAnswerPreview = preview(message.Content)
			}
		}
		if status.LastUserPreview != "" && status.LastAnswerPreview != "" {
			break
		}
	}
	return status, nil
}

func (s *Service) Rename(ctx context.Context, scope Scope, sessionID, title string) (*storage.Session, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, fmt.Errorf("title is required")
	}
	session, err := s.targetSession(ctx, scope, sessionID)
	if err != nil {
		return nil, err
	}
	metadata, err := renameMetadata(session.Metadata)
	if err != nil {
		return nil, err
	}
	session.Title = title
	session.Metadata = metadata
	session.UpdatedAt = storage.Now()
	if err := s.store.Sessions().Update(ctx, session); err != nil {
		return nil, err
	}
	return session, nil
}

func renameMetadata(raw string) (string, error) {
	metadata := map[string]any{}
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
			return "", fmt.Errorf("decode session metadata: %w", err)
		}
	}
	metadata["title_renamed"] = true
	metadata["title_source"] = "manual"
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return "", fmt.Errorf("encode session metadata: %w", err)
	}
	return string(encoded), nil
}

func (s *Service) Archive(ctx context.Context, scope Scope, sessionID string) (*storage.Session, error) {
	session, err := s.targetSession(ctx, scope, sessionID)
	if err != nil {
		return nil, err
	}
	if session.ArchivedAt == nil {
		now := storage.Now()
		session.ArchivedAt = &now
		session.UpdatedAt = now
		if err := s.store.Sessions().Update(ctx, session); err != nil {
			return nil, err
		}
	}
	return session, nil
}

func (s *Service) Unarchive(ctx context.Context, scope Scope, sessionID string) (*storage.Session, error) {
	session, err := s.targetSession(ctx, scope, sessionID)
	if err != nil {
		return nil, err
	}
	if session.ArchivedAt != nil {
		session.ArchivedAt = nil
		session.UpdatedAt = storage.Now()
		if err := s.store.Sessions().Update(ctx, session); err != nil {
			return nil, err
		}
	}
	s.setCurrent(scope, session.ID)
	return session, nil
}

func (s *Service) Pin(ctx context.Context, scope Scope, sessionID string) (*storage.Session, error) {
	session, err := s.targetSession(ctx, scope, sessionID)
	if err != nil {
		return nil, err
	}
	if session.PinnedAt == nil {
		now := storage.Now()
		session.PinnedAt = &now
		session.UpdatedAt = now
		if err := s.store.Sessions().Update(ctx, session); err != nil {
			return nil, err
		}
	}
	return session, nil
}

func (s *Service) Unpin(ctx context.Context, scope Scope, sessionID string) (*storage.Session, error) {
	session, err := s.targetSession(ctx, scope, sessionID)
	if err != nil {
		return nil, err
	}
	if session.PinnedAt != nil {
		session.PinnedAt = nil
		session.UpdatedAt = storage.Now()
		if err := s.store.Sessions().Update(ctx, session); err != nil {
			return nil, err
		}
	}
	return session, nil
}

func (s *Service) Delete(ctx context.Context, scope Scope, sessionID string) error {
	session, err := s.targetSession(ctx, scope, sessionID)
	if err != nil {
		return err
	}
	if err := s.store.Sessions().Delete(ctx, session.ID); err != nil {
		return err
	}
	s.clearCurrentIf(scope, session.ID)
	return nil
}

func (s *Service) CleanupExpired(ctx context.Context, cutoff time.Time) (int, error) {
	return s.store.Sessions().DeleteExpired(ctx, cutoff)
}

func (s *Service) targetSession(ctx context.Context, scope Scope, sessionID string) (*storage.Session, error) {
	sessionID = strings.TrimSpace(sessionID)
	var session *storage.Session
	var err error
	if sessionID == "" {
		session, err = s.Current(ctx, scope)
	} else {
		session, err = s.store.Sessions().Get(ctx, sessionID)
	}
	if err != nil {
		return nil, err
	}
	if !s.canAccess(scope, session) {
		return nil, fmt.Errorf("session %s is not in current platform scope", session.ID)
	}
	return session, nil
}

func (s *Service) Touch(ctx context.Context, session *storage.Session) error {
	latest, err := s.store.Sessions().Get(ctx, session.ID)
	if err != nil {
		return err
	}
	latest.UpdatedAt = storage.Now()
	return s.store.Sessions().Update(ctx, latest)
}

func (s *Service) MaybeScheduleNaming(ctx context.Context, sessionID string) {
	if s.titleRenamed(ctx, sessionID) {
		return
	}
	if s.titleGen == nil {
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

	// TODO: 将来如果接入 TUI 或后台任务队列，这里可以替换为可观察、可取消的任务调度器。
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
	// TODO: 未来可以将最大失败次数做成配置项，并按需持久化命名状态。
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
			return
		}
	}
	// TODO: 后续添加 /rename <title> 命令；手动命名后应避免后台自动命名覆盖。
	s.markNamingDone(session.ID)
}

func (s *Service) notifyNamingScheduled(ctx context.Context, event NamingScheduledEvent) {
	if s.notifier == nil {
		return
	}
	s.notifier.NotifyNamingScheduled(ctx, event)
}

func (s *Service) notifyNamingCompleted(ctx context.Context, event NamingCompletedEvent) {
	if s.notifier == nil {
		return
	}
	s.notifier.NotifyNamingCompleted(ctx, event)
}

func (s *Service) notifyNamingFailed(ctx context.Context, event NamingFailedEvent) {
	if s.notifier == nil {
		return
	}
	s.notifier.NotifyNamingFailed(ctx, event)
}

func filterConversationMessages(messages []storage.Message) []storage.Message {
	out := make([]storage.Message, 0, len(messages))
	for _, message := range messages {
		if message.Role != storage.RoleUser && message.Role != storage.RoleAssistant {
			continue
		}
		if strings.TrimSpace(message.Content) == "" {
			continue
		}
		out = append(out, message)
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

func (s *Service) setCurrent(scope Scope, sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current[s.scopeKey(scope)] = sessionID
}

func (s *Service) clearCurrentIf(scope Scope, sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := s.scopeKey(scope)
	if s.current[key] == sessionID {
		delete(s.current, key)
	}
}

func (s *Service) scopeKey(scope Scope) string {
	return scope.ActorID + "\x00" + scope.Platform + "\x00" + scope.PlatformScopeID
}

func (s *Service) canAccess(scope Scope, session *storage.Session) bool {
	if scope.IsCLI {
		return true
	}
	if session.OwnerID != scope.ActorID || session.Platform != scope.Platform {
		return false
	}
	return session.PlatformScopeID == scope.PlatformScopeID || isCronSession(session)
}

func isCronSession(session *storage.Session) bool {
	return session != nil && strings.HasPrefix(session.PlatformScopeID, "cron:")
}

func forkTitle(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "New session"
	}
	return defaultTitle("Fork: " + title)
}

func defaultTitle(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "New session"
	}
	const maxRunes = 40
	if utf8.RuneCountInString(text) <= maxRunes {
		return text
	}
	runes := []rune(text)
	return string(runes[:maxRunes]) + "..."
}

func preview(text string) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\n", " "))
	const maxRunes = 80
	if utf8.RuneCountInString(text) <= maxRunes {
		return text
	}
	runes := []rune(text)
	return string(runes[:maxRunes]) + "..."
}
