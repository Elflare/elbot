package workspace

import "context"

type SessionStateStore interface {
	WorkspaceStore
	GetWorkspaceState(context.Context) (State, error)
	SetUserWorkspaceDir(context.Context, string) error
	SetWorkspaceSync(context.Context, bool, bool, string) (State, error)
}

type SessionStateStoreFactory func(sessionID string) SessionStateStore
type CurrentSessionID func(context.Context) (string, error)

type Service struct {
	currentSessionID CurrentSessionID
	storeForSession  SessionStateStoreFactory
}

func NewService(currentSessionID CurrentSessionID, storeForSession SessionStateStoreFactory) *Service {
	return &Service{currentSessionID: currentSessionID, storeForSession: storeForSession}
}

func (s *Service) GetCurrentState(ctx context.Context) (string, State, error) {
	sessionID, err := s.currentSessionID(ctx)
	if err != nil {
		return "", State{}, err
	}
	state, err := s.getSessionState(ctx, sessionID)
	return sessionID, state, err
}

func (s *Service) getSessionState(ctx context.Context, sessionID string) (State, error) {
	store := s.storeForSession(sessionID)
	state, err := store.GetWorkspaceState(ctx)
	if err != nil {
		return State{}, err
	}
	state.Dir, err = resolveWorkspaceDir(ctx, store, state.Dir)
	return state, err
}

func (s *Service) SetUserDir(ctx context.Context, dir string) (string, State, error) {
	sessionID, err := s.currentSessionID(ctx)
	if err != nil {
		return "", State{}, err
	}
	store := s.storeForSession(sessionID)
	if err := store.SetUserWorkspaceDir(ctx, dir); err != nil {
		return "", State{}, err
	}
	state, err := s.getSessionState(ctx, sessionID)
	return sessionID, state, err
}

func (s *Service) SetSync(ctx context.Context, agentToUser, userToAgent bool) (string, State, error) {
	sessionID, err := s.currentSessionID(ctx)
	if err != nil {
		return "", State{}, err
	}
	store := s.storeForSession(sessionID)
	state, err := store.GetWorkspaceState(ctx)
	if err != nil {
		return "", State{}, err
	}
	agentDir, err := resolveWorkspaceDir(ctx, store, state.Dir)
	if err != nil {
		return "", State{}, err
	}
	state, err = store.SetWorkspaceSync(ctx, agentToUser, userToAgent, agentDir)
	if err != nil {
		return "", State{}, err
	}
	state.Dir, err = resolveWorkspaceDir(ctx, store, state.Dir)
	return sessionID, state, err
}
