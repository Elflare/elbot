package workspace

import "context"

type SessionStateStore interface {
	WorkspaceStore
	GetWorkspaceState(context.Context) (State, error)
	SetUserWorkspaceDir(context.Context, string) error
	SetWorkspaceSync(context.Context, bool, bool) (State, error)
}

type SessionStateStoreFactory func(sessionID string) SessionStateStore

type Service struct {
	storeForSession SessionStateStoreFactory
}

func NewService(storeForSession SessionStateStoreFactory) *Service {
	return &Service{storeForSession: storeForSession}
}

func (s *Service) GetSessionState(ctx context.Context, sessionID string) (State, error) {
	store := s.storeForSession(sessionID)
	state, err := store.GetWorkspaceState(ctx)
	if err != nil {
		return State{}, err
	}
	state.Dir, err = resolveWorkspaceDir(ctx, store, state.Dir)
	return state, err
}

func (s *Service) SetUserDir(ctx context.Context, sessionID, dir string) (State, error) {
	store := s.storeForSession(sessionID)
	if err := store.SetUserWorkspaceDir(ctx, dir); err != nil {
		return State{}, err
	}
	return s.GetSessionState(ctx, sessionID)
}

func (s *Service) SetSync(ctx context.Context, sessionID string, agentToUser, userToAgent bool) (State, error) {
	store := s.storeForSession(sessionID)
	state, err := store.SetWorkspaceSync(ctx, agentToUser, userToAgent)
	if err != nil {
		return State{}, err
	}
	state.Dir, err = resolveWorkspaceDir(ctx, store, state.Dir)
	return state, err
}
