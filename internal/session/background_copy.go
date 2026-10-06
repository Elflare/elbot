package session

import (
	"context"
	"fmt"

	"elbot/internal/storage"
)

// BackgroundCopyRequest copies task history without activating a foreground
// binding or inheriting execution/tool state. Metadata contains business fields;
// naming and background identity are always supplied by Session.
type BackgroundCopyRequest struct {
	SourceSessionID string
	Kind            string
	Name            string
	Title           string
	Metadata        storage.SessionMetadata
}

func (s *Service) CopyBackground(ctx context.Context, target Scope, req BackgroundCopyRequest) (*storage.Session, error) {
	locked, release, err := s.EnterSessions(ctx, req.SourceSessionID)
	if err != nil {
		return nil, err
	}
	source, err := s.store.Sessions().Get(locked, req.SourceSessionID)
	if err == nil && (WasPromoted(source) || !IsBackground(source)) {
		err = ErrForegroundSession
	}
	if err == nil {
		err = s.requireIdle(source.ID)
	}
	release()
	if err != nil {
		return nil, err
	}

	prepared, err := s.prepareMaterial(ctx, source, nil)
	if err != nil {
		return nil, err
	}
	nextID := storage.NewID()
	locked, release, err = s.EnterSessions(ctx, source.ID, nextID)
	if err != nil {
		return nil, err
	}
	defer release()
	latest, err := s.store.Sessions().Get(locked, source.ID)
	if err != nil {
		return nil, err
	}
	if WasPromoted(latest) || !IsBackground(latest) {
		return nil, ErrForegroundSession
	}
	if err := s.requireIdle(source.ID); err != nil {
		return nil, err
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
	if prepared != nil {
		pending, err := s.store.Dialogues().PendingInputs(locked, source.ID)
		if err != nil {
			return nil, err
		}
		if len(pending) > 0 {
			return nil, fmt.Errorf("background copy requires a complete committed native window")
		}
	}
	messages, err := s.store.Messages().ListBySession(locked, source.ID)
	if err != nil {
		return nil, err
	}
	seed, err := req.Metadata.Encode()
	if err != nil {
		return nil, err
	}
	seed, err = InheritOrigin(latest, seed)
	if err != nil {
		return nil, err
	}
	background := BackgroundRequest{Kind: req.Kind, Name: req.Name, Title: req.Title}
	metadata, err := backgroundMetadata(seed, background)
	if err != nil {
		return nil, err
	}
	title := req.Title
	if title == "" {
		title = backgroundTitle(req.Kind, req.Name)
	}
	row := &storage.Session{ID: nextID, OwnerID: target.ActorID, Platform: target.Platform, PlatformScopeID: target.PlatformScopeID,
		Mode: storage.SessionModeBackground, Title: title, Status: storage.SessionStatusActive, Metadata: metadata}
	copies := make([]*storage.Message, 0, len(messages))
	for _, original := range messages {
		msg := original
		msg.ID, msg.SessionID = storage.NewID(), row.ID
		msg.ParentMessageID, msg.ReplyToMessageID, msg.ReplyToPlatformMessageID = "", "", ""
		copies = append(copies, &msg)
	}
	if err := locked.Err(); err != nil {
		return nil, err
	}
	if err := s.store.Sessions().CreateMaterial(locked, materialCreate(row, latest, prepared, copies)); err != nil {
		return nil, err
	}
	return row, nil
}
