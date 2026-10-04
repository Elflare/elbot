package session

import (
	"context"
	"fmt"
	"strings"
	"time"

	"elbot/internal/storage"
)

func (s *Service) Rename(ctx context.Context, scope Scope, sessionID, title string) (*storage.Session, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, fmt.Errorf("title is required")
	}
	target, err := s.targetSession(ctx, scope, sessionID)
	if err != nil {
		return nil, err
	}
	return s.store.Sessions().Mutate(ctx, target.ID, func(row *storage.Session) error {
		if !s.canAccess(scope, row) {
			return fmt.Errorf("session is not in current platform scope")
		}
		metadata, err := renameMetadata(row.Metadata)
		if err != nil {
			return err
		}
		row.Title, row.Metadata, row.UpdatedAt = title, metadata, storage.Now()
		return nil
	})
}

func renameMetadata(raw string) (string, error) {
	metadata, err := storage.DecodeSessionMetadata(raw)
	if err != nil {
		return "", err
	}
	if err := metadata.Set("title_renamed", true); err != nil {
		return "", err
	}
	if err := metadata.Set("title_source", "manual"); err != nil {
		return "", err
	}
	return metadata.Encode()
}

func (s *Service) Archive(ctx context.Context, scope Scope, sessionID string) (*storage.Session, error) {
	target, err := s.targetSession(ctx, scope, sessionID)
	if err != nil {
		return nil, err
	}
	row, err := s.store.Sessions().Mutate(ctx, target.ID, func(row *storage.Session) error {
		if !s.canAccess(scope, row) {
			return fmt.Errorf("session is not in current platform scope")
		}
		if row.ArchivedAt == nil {
			now := storage.Now()
			row.ArchivedAt = &now
			row.UpdatedAt = now
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return row, nil
}

func (s *Service) Unarchive(ctx context.Context, scope Scope, sessionID string) (*storage.Session, error) {
	ctx, release, err := s.EnterScope(ctx, scope)
	if err != nil {
		return nil, err
	}
	defer release()
	row, err := s.targetSession(ctx, scope, sessionID)
	if err != nil {
		return nil, err
	}
	return s.activateExisting(ctx, scope, row.ID, true)
}

func (s *Service) Pin(ctx context.Context, scope Scope, sessionID string) (*storage.Session, error) {
	target, err := s.targetSession(ctx, scope, sessionID)
	if err != nil {
		return nil, err
	}
	row, err := s.store.Sessions().Mutate(ctx, target.ID, func(row *storage.Session) error {
		if !s.canAccess(scope, row) {
			return fmt.Errorf("session is not in current platform scope")
		}
		if row.PinnedAt == nil {
			now := storage.Now()
			row.PinnedAt = &now
			row.UpdatedAt = now
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return row, nil
}

func (s *Service) Unpin(ctx context.Context, scope Scope, sessionID string) (*storage.Session, error) {
	target, err := s.targetSession(ctx, scope, sessionID)
	if err != nil {
		return nil, err
	}
	row, err := s.store.Sessions().Mutate(ctx, target.ID, func(row *storage.Session) error {
		if !s.canAccess(scope, row) {
			return fmt.Errorf("session is not in current platform scope")
		}
		if row.PinnedAt != nil {
			now := storage.Now()
			row.PinnedAt = nil
			row.UpdatedAt = now
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return row, nil
}

func (s *Service) Delete(ctx context.Context, scope Scope, sessionID string) error {
	ctx, release, err := s.EnterActivation(ctx, scope, sessionID)
	if err != nil {
		return err
	}
	defer release()
	row, err := s.targetSession(ctx, scope, sessionID)
	if err != nil {
		return err
	}
	if err := s.requireIdle(row.ID); err != nil {
		return err
	}
	if err := s.store.Sessions().Delete(ctx, row.ID); err != nil {
		return err
	}
	s.clearSessionBindings(ctx, row.ID, ChangeDelete)
	return nil
}

func (s *Service) CleanupExpired(ctx context.Context, cutoff time.Time) (int, error) {
	ids, err := s.store.Sessions().ListExpiredIDs(ctx, cutoff)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, id := range ids {
		locked, release, err := s.EnterSessions(ctx, id)
		if err != nil {
			return count, err
		}
		if s.requireIdle(id) != nil {
			release()
			continue
		}
		deleted, err := s.store.Sessions().DeleteIfExpired(locked, id, cutoff)
		if err == nil && deleted {
			s.clearSessionBindings(locked, id, ChangeCleanup)
			count++
		}
		release()
		if err != nil {
			return count, err
		}
	}
	return count, nil
}

func (s *Service) targetSession(ctx context.Context, scope Scope, sessionID string) (*storage.Session, error) {
	sessionID = strings.TrimSpace(sessionID)
	var session *storage.Session
	var err error
	if sessionID == "" {
		session, err = s.Current(ctx, scope)
	} else {
		session, err = s.store.Sessions().Get(ctx, sessionID)
	}
	if err != nil {
		return nil, err
	}
	if !s.canAccess(scope, session) {
		return nil, fmt.Errorf("session %s is not in current platform scope", session.ID)
	}
	return session, nil
}
