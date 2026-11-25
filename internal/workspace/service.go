package workspace

import (
	"context"
	"fmt"

	"elbot/internal/operationresult"
)

type SessionStateStore interface {
	WorkspaceStore
	GetWorkspaceState(context.Context) (State, error)
	SetUserWorkspaceDir(context.Context, string) error
	SetWorkspaceSync(context.Context, bool, bool, string) error
}

type SessionStateStoreFactory func(sessionID string) SessionStateStore
type CurrentSessionID func(context.Context) (operationresult.Result[string], error)

type CurrentState struct {
	SessionID string
	State     State
}

type Service struct {
	currentSessionID CurrentSessionID
	storeForSession  SessionStateStoreFactory
}

func NewService(currentSessionID CurrentSessionID, storeForSession SessionStateStoreFactory) *Service {
	return &Service{currentSessionID: currentSessionID, storeForSession: storeForSession}
}

func (s *Service) GetCurrentState(ctx context.Context) (operationresult.Result[CurrentState], error) {
	current, err := s.currentSessionID(ctx)
	if err != nil {
		return operationresult.Result[CurrentState]{}, err
	}
	if current.Status != operationresult.Success {
		return operationresult.Result[CurrentState]{Status: current.Status, Message: current.Message}, nil
	}
	return s.sessionStateResult(ctx, current.Data)
}

func (s *Service) sessionStateResult(ctx context.Context, sessionID string) (operationresult.Result[CurrentState], error) {
	state, err := s.getSessionState(ctx, sessionID)
	if err != nil {
		return operationresult.Result[CurrentState]{}, err
	}
	return operationresult.Result[CurrentState]{Status: operationresult.Success, Data: CurrentState{SessionID: sessionID, State: state}}, nil
}

func (s *Service) getSessionState(ctx context.Context, sessionID string) (State, error) {
	store := s.storeForSession(sessionID)
	for {
		state, err := store.GetWorkspaceState(ctx)
		if err != nil {
			return State{}, err
		}
		dir, missing, err := resolveSavedDir(state.Dir)
		if err != nil {
			return State{}, err
		}
		if !missing {
			state.Dir = dir
			return state, nil
		}
		if _, err := store.ClearWorkspaceDirIfMatches(ctx, state.Dir); err != nil {
			return State{}, fmt.Errorf("clear missing workspace: %w", err)
		}
		// Reload all fields after clearing or a concurrent selection.
	}
}

func (s *Service) SetUserDir(ctx context.Context, dir string) (operationresult.Result[CurrentState], error) {
	current, err := s.currentSessionID(ctx)
	if err != nil {
		return operationresult.Result[CurrentState]{}, err
	}
	if current.Status != operationresult.Success {
		return operationresult.Result[CurrentState]{Status: current.Status, Message: current.Message}, nil
	}
	sessionID := current.Data
	store := s.storeForSession(sessionID)
	if err := store.SetUserWorkspaceDir(ctx, dir); err != nil {
		return operationresult.Result[CurrentState]{}, err
	}
	return s.sessionStateResult(ctx, sessionID)
}

func (s *Service) SetSync(ctx context.Context, agentToUser, userToAgent bool) (operationresult.Result[CurrentState], error) {
	current, err := s.currentSessionID(ctx)
	if err != nil {
		return operationresult.Result[CurrentState]{}, err
	}
	if current.Status != operationresult.Success {
		return operationresult.Result[CurrentState]{Status: current.Status, Message: current.Message}, nil
	}
	sessionID := current.Data
	store := s.storeForSession(sessionID)
	state, err := s.getSessionState(ctx, sessionID)
	if err != nil {
		return operationresult.Result[CurrentState]{}, err
	}
	if err := store.SetWorkspaceSync(ctx, agentToUser, userToAgent, state.Dir); err != nil {
		return operationresult.Result[CurrentState]{}, err
	}
	return s.sessionStateResult(ctx, sessionID)
}
