package session

import (
	"context"
	"fmt"

	"elbot/internal/storage"
)

// CompactedRequest carries content prepared by the context owner. Session owns
// inheritance, naming metadata, persistence and foreground activation.
type CompactedRequest struct {
	ID                   string
	Title                string
	Metadata             string
	Seed                 *storage.NativeSeed
	ExpectedCheckpointID string
}

func (s *Service) CreateCompacted(ctx context.Context, scope Scope, sourceID string, req CompactedRequest) (*storage.Session, error) {
	if req.ID == "" || req.ID == sourceID {
		return nil, fmt.Errorf("a distinct preallocated session ID is required")
	}
	source, err := s.store.Sessions().Get(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	background := IsBackground(source)
	var release func()
	if background {
		ctx, release, err = s.EnterSessions(ctx, sourceID, req.ID)
	} else {
		ctx, release, err = s.EnterActivation(ctx, scope, sourceID, req.ID)
	}
	if err != nil {
		return nil, err
	}
	defer release()
	source, err = s.store.Sessions().Get(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	if background != IsBackground(source) {
		return nil, ErrForegroundSession
	}
	if !s.canAccess(scope, source) {
		return nil, fmt.Errorf("session %s is not in current platform scope", sourceID)
	}
	if !background {
		_, binding, err := s.CurrentBound(ctx, scope)
		if err != nil {
			return nil, err
		}
		original, hasOriginal := BindingFromContext(ctx)
		if binding.SessionID() != sourceID || (hasOriginal && (original != binding || !original.Valid())) {
			return nil, fmt.Errorf("session binding expired")
		}
	}
	metadata, err := InheritOrigin(source, req.Metadata)
	if err != nil {
		return nil, err
	}
	fields, err := storage.DecodeSessionMetadata(metadata)
	if err != nil {
		return nil, err
	}
	if err := fields.Set("title_renamed", true); err != nil {
		return nil, err
	}
	if err := fields.Set("title_source", "compact"); err != nil {
		return nil, err
	}
	metadata, err = fields.Encode()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	next := &storage.Session{ID: req.ID, OwnerID: source.OwnerID, Platform: source.Platform,
		PlatformScopeID: source.PlatformScopeID, Title: req.Title, Mode: source.Mode,
		Status: storage.SessionStatusActive, Metadata: metadata}
	var saveErr error
	if req.Seed != nil {
		saveErr = s.store.Sessions().CreateMaterial(ctx, storage.SessionMaterialCreate{Session: next, Seed: req.Seed, SourceSessionID: sourceID, ExpectedCheckpointID: req.ExpectedCheckpointID})
	} else {
		saveErr = s.store.Sessions().Create(ctx, next)
	}
	if saveErr != nil {
		return nil, saveErr
	}
	if !background {
		s.setCurrent(ctx, scope, next.ID, ChangeCreate)
	}
	return next, nil
}
