package session

import (
	"context"
	"fmt"
	"strings"

	"elbot/internal/storage"
)

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
