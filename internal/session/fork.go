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
	locked, release, err := s.EnterActivation(ctx, scope, message.SessionID)
	if err != nil {
		return nil, err
	}
	original := s.currentBinding(scope)
	if binding, ok := BindingFromContext(ctx); ok && (binding != original || !binding.Valid()) {
		release()
		return nil, fmt.Errorf("session binding expired")
	}
	if err := s.canReplaceCurrent(scope, ""); err != nil {
		release()
		return nil, err
	}
	source, err := s.store.Sessions().Get(locked, message.SessionID)
	if err == nil && !s.canAccess(scope, source) {
		err = fmt.Errorf("session %s is not in current platform scope", source.ID)
	}
	release()
	if err != nil {
		return nil, err
	}

	prepared, err := s.prepareMaterial(ctx, source, message)
	if err != nil {
		return nil, err
	}
	nextID := storage.NewID()
	locked, release, err = s.EnterActivation(ctx, scope, source.ID, nextID)
	if err != nil {
		return nil, err
	}
	defer release()
	if s.currentBinding(scope) != original || original != nil && !original.Valid() {
		return nil, fmt.Errorf("session binding expired")
	}
	if err := s.canReplaceCurrent(scope, ""); err != nil {
		return nil, err
	}
	latest, err := s.store.Sessions().Get(locked, source.ID)
	if err != nil {
		return nil, err
	}
	if !s.canAccess(scope, latest) {
		return nil, fmt.Errorf("session %s is not in current platform scope", source.ID)
	}
	before, _, err := Origin(source)
	if err != nil {
		return nil, err
	}
	after, _, err := Origin(latest)
	if err != nil {
		return nil, err
	}
	if before != after {
		return nil, fmt.Errorf("source session origin changed")
	}
	metadata, err := InheritOrigin(latest, "")
	if err != nil {
		return nil, err
	}
	fork := &storage.Session{ID: nextID, ParentSessionID: source.ID, ForkFromMessageID: message.ID,
		OwnerID: scope.ActorID, Platform: scope.Platform, PlatformScopeID: scope.PlatformScopeID,
		Mode: latest.Mode, Status: storage.SessionStatusActive, Title: forkTitle(latest.Title), Metadata: metadata}
	if err := locked.Err(); err != nil {
		return nil, err
	}
	if prepared != nil {
		err = s.store.Sessions().CreateMaterial(locked, materialCreate(fork, latest, prepared, nil))
	} else {
		err = s.store.Sessions().Create(locked, fork)
	}
	if err != nil {
		return nil, err
	}
	s.setCurrent(locked, scope, fork.ID, ChangeFork)
	return fork, nil
}
