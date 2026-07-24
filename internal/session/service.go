package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"elbot/internal/storage"
)

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
	current, err := s.Current(ctx, scope)
	if err == nil {
		return current, nil
	}
	if !errors.Is(err, storage.ErrNotFound) {
		return nil, err
	}

	return s.Create(ctx, scope, CreateRequest{Title: defaultTitle(firstMessage)})
}

func (s *Service) Create(ctx context.Context, scope Scope, req CreateRequest) (*storage.Session, error) {
	if req.Mode == "" {
		req.Mode = s.defaultMode
	}
	if err := validateMode(req.Mode); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Title) == "" {
		req.Title = "New session"
	}
	session := &storage.Session{
		OwnerID:         scope.ActorID,
		Platform:        scope.Platform,
		PlatformScopeID: scope.PlatformScopeID,
		Mode:            req.Mode,
		Status:          storage.SessionStatusActive,
		Title:           req.Title,
		Metadata:        req.Metadata,
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

func (s *Service) Resume(ctx context.Context, scope Scope, sessionID string) (*storage.Session, error) {
	session, err := s.store.Sessions().Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if !s.canAccess(scope, session) {
		return nil, fmt.Errorf("session %s is not in current platform scope", sessionID)
	}
	session.UpdatedAt = storage.Now()
	if err := s.store.Sessions().Update(ctx, session); err != nil {
		return nil, err
	}
	s.setCurrent(scope, session.ID)
	return session, nil
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

func (s *Service) Touch(ctx context.Context, session *storage.Session) error {
	latest, err := s.store.Sessions().Get(ctx, session.ID)
	if err != nil {
		return err
	}
	latest.UpdatedAt = storage.Now()
	return s.store.Sessions().Update(ctx, latest)
}

func (s *Service) ResetCurrent(scope Scope) {
	s.clearCurrentIf(scope, "")
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
	if sessionID == "" || s.current[key] == sessionID {
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
	return session.PlatformScopeID == scope.PlatformScopeID || isBackgroundSession(session)
}

func isBackgroundSession(session *storage.Session) bool {
	if session == nil {
		return false
	}
	scopeID := strings.TrimSpace(session.PlatformScopeID)
	return strings.HasPrefix(scopeID, "cron:") || strings.HasPrefix(scopeID, "elnis:")
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
