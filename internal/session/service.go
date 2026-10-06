package session

import (
	"context"
	"errors"
	"strings"
	"sync"
	"unicode/utf8"

	"elbot/internal/signal"
	"elbot/internal/storage"
)

type Service struct {
	store                storage.Store
	mu                   sync.Mutex
	current              map[string]*Binding
	gates                map[string]*scopeGate
	changed              *signal.Signal[BindingChangedEvent]
	activeSessionIDs     func() []string
	foregroundActivation func(context.Context, *storage.Session, *Binding)
	foregroundCheck      func(context.Context, *storage.Session) error
	materials            MaterialResolver
	namingConfig         NamingConfig
	titleGen             TitleGenerator
	namingSignals        NamingSignals
	namingStates         map[string]namingState
	naming               namingLifecycle
	defaultMode          string
}

func NewService(store storage.Store) *Service {
	return NewServiceWithNaming(store, NamingConfig{TriggerStep: 1}, nil)
}

func NewServiceWithNaming(store storage.Store, cfg NamingConfig, titleGen TitleGenerator) *Service {
	return NewServiceWithConfig(store, Config{NamingConfig: cfg, DefaultMode: storage.SessionModeWork}, titleGen)
}

func NewServiceWithConfig(store storage.Store, cfg Config, titleGen TitleGenerator) *Service {
	if cfg.TriggerStep <= 0 {
		cfg.TriggerStep = 1
	}
	if cfg.DefaultMode == "" {
		cfg.DefaultMode = storage.SessionModeWork
	}
	if err := validateMode(cfg.DefaultMode); err != nil {
		cfg.DefaultMode = storage.SessionModeWork
	}
	return &Service{
		store:         store,
		current:       map[string]*Binding{},
		gates:         map[string]*scopeGate{},
		changed:       signal.New[BindingChangedEvent]("session.binding_changed", nil),
		namingConfig:  cfg.NamingConfig,
		titleGen:      titleGen,
		namingSignals: newNamingSignals(),
		namingStates:  map[string]namingState{},
		naming:        namingLifecycle{done: make(chan struct{})},
		defaultMode:   cfg.DefaultMode,
	}
}

func (s *Service) GetOrCreateCurrent(ctx context.Context, scope Scope, firstMessage string) (*storage.Session, error) {
	ctx, release, err := s.EnterActivation(ctx, scope)
	if err != nil {
		return nil, err
	}
	defer release()
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
	ctx, release, err := s.EnterActivation(ctx, scope)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := s.canReplaceCurrent(scope, ""); err != nil {
		return nil, err
	}
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
		ID:              req.ID,
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
	s.setCurrent(ctx, scope, session.ID, ChangeCreate)
	return session, nil
}

func (s *Service) DefaultMode() string {
	if s.defaultMode == "" {
		return storage.SessionModeWork
	}
	return s.defaultMode
}

func (s *Service) Resume(ctx context.Context, scope Scope, sessionID string) (*storage.Session, error) {
	return s.activateExisting(ctx, scope, sessionID, false)
}

func (s *Service) Current(ctx context.Context, scope Scope) (*storage.Session, error) {
	row, _, err := s.CurrentBound(ctx, scope)
	return row, err
}

func (s *Service) Touch(ctx context.Context, session *storage.Session) error {
	_, err := s.store.Sessions().Mutate(ctx, session.ID, func(row *storage.Session) error { row.UpdatedAt = storage.Now(); return nil })
	return err
}

func (s *Service) ResetCurrent(ctx context.Context, scope Scope) error {
	ctx, release, err := s.EnterActivation(ctx, scope)
	if err != nil {
		return err
	}
	defer release()
	if err := s.canReplaceCurrent(scope, ""); err != nil {
		return err
	}
	s.setCurrent(ctx, scope, "", ChangeReset)
	return nil
}

func (s *Service) scopeKey(scope Scope) string {
	return scope.Key()
}

func (s *Service) canAccess(scope Scope, session *storage.Session) bool {
	if scope.IsCLI {
		return true
	}
	if session.OwnerID != scope.ActorID || session.Platform != scope.Platform {
		return false
	}
	return session.PlatformScopeID == scope.PlatformScopeID || (scope.acceptsBackground() && IsBackground(session))
}

func isBackgroundSession(row *storage.Session) bool { return IsBackground(row) }

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
