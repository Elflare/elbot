package session

import (
	"context"
	"strings"

	"elbot/internal/storage"
)

// BackgroundRequest describes a task session without activating a foreground
// binding. Tool state belongs to toolrun and is not part of this request.
type BackgroundRequest struct {
	SessionID string
	Kind      string
	Name      string
	Title     string
	Metadata  map[string]string
}

func (s *Service) PrepareBackground(ctx context.Context, scope Scope, req BackgroundRequest) (*storage.Session, error) {
	if req.SessionID != "" {
		locked, release, err := s.EnterSessions(ctx, req.SessionID)
		if err != nil {
			return nil, err
		}
		defer release()
		return s.store.Sessions().Mutate(locked, req.SessionID, func(row *storage.Session) error {
			if WasPromoted(row) {
				return ErrForegroundSession
			}
			metadata, err := backgroundMetadata(row.Metadata, req)
			if err != nil {
				return err
			}
			row.Mode, row.Metadata, row.UpdatedAt = storage.SessionModeBackground, metadata, storage.Now()
			return nil
		})
	}
	metadata, err := backgroundMetadata("", req)
	if err != nil {
		return nil, err
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = backgroundTitle(req.Kind, req.Name)
	}
	row := &storage.Session{OwnerID: scope.ActorID, Platform: scope.Platform, PlatformScopeID: scope.PlatformScopeID,
		Mode: storage.SessionModeBackground, Title: title, Status: storage.SessionStatusActive, Metadata: metadata}
	if err := s.store.Sessions().Create(ctx, row); err != nil {
		return nil, err
	}
	return row, nil
}

func backgroundMetadata(raw string, req BackgroundRequest) (string, error) {
	fields, err := storage.DecodeSessionMetadata(raw)
	if err != nil {
		return "", err
	}
	for key, value := range map[string]any{"title_renamed": true, "title_source": req.Kind, "background_kind": req.Kind, "background_name": req.Name} {
		if err := fields.Set(key, value); err != nil {
			return "", err
		}
	}
	for key, value := range req.Metadata {
		if strings.TrimSpace(key) != "" {
			if err := fields.Set(key, value); err != nil {
				return "", err
			}
		}
	}
	return fields.Encode()
}

func backgroundTitle(kind, name string) string {
	kind, name = strings.TrimSpace(kind), strings.TrimSpace(name)
	if kind == "" {
		kind = "Background"
	}
	title := strings.ToUpper(kind[:1]) + kind[1:]
	if name != "" {
		title += ": " + name
	}
	return title
}
