package session

import (
	"context"
	"errors"
	"fmt"

	"elbot/internal/storage"
)

var ErrChatModeRequiresEmptySession = errors.New("chat mode requires an empty work session")

func (s *Service) ActivateMode(ctx context.Context, scope Scope, req ActivateModeRequest) (ActivateModeResult, error) {
	ctx, release, enterErr := s.EnterActivation(ctx, scope)
	if enterErr != nil {
		return ActivateModeResult{}, enterErr
	}
	defer release()

	if err := s.canReplaceCurrent(scope, ""); err != nil {
		return ActivateModeResult{}, err
	}
	if err := validateMode(req.Mode); err != nil {
		return ActivateModeResult{}, err
	}
	session, err := s.Current(ctx, scope)
	if err != nil {
		if !errors.Is(err, storage.ErrNotFound) {
			return ActivateModeResult{}, err
		}
		session, err = s.Create(ctx, scope, CreateRequest{Title: req.NewSessionTitle, Mode: req.Mode})
		if err != nil {
			return ActivateModeResult{}, err
		}
		return ActivateModeResult{Session: session}, nil
	}
	if session.Mode == req.Mode {
		return ActivateModeResult{Session: session, AlreadyActive: true}, nil
	}
	if req.Mode == storage.SessionModeChat {
		messages, err := s.store.Messages().ListBySession(ctx, session.ID)
		if err != nil {
			return ActivateModeResult{}, err
		}
		if len(messages) > 0 {
			return ActivateModeResult{}, ErrChatModeRequiresEmptySession
		}
	}
	session, err = s.store.Sessions().Mutate(ctx, session.ID, func(row *storage.Session) error { row.Mode = req.Mode; row.UpdatedAt = storage.Now(); return nil })
	if err != nil {
		return ActivateModeResult{}, err
	}
	return ActivateModeResult{Session: session}, nil
}

func validateMode(mode string) error {
	if mode == storage.SessionModeWork || mode == storage.SessionModeChat {
		return nil
	}
	return fmt.Errorf("invalid session mode %q", mode)
}
