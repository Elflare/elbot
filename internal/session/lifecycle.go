package session

import (
	"context"
	"encoding/json"
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
	session, err := s.targetSession(ctx, scope, sessionID)
	if err != nil {
		return nil, err
	}
	metadata, err := renameMetadata(session.Metadata)
	if err != nil {
		return nil, err
	}
	session.Title = title
	session.Metadata = metadata
	session.UpdatedAt = storage.Now()
	if err := s.store.Sessions().Update(ctx, session); err != nil {
		return nil, err
	}
	return session, nil
}

func renameMetadata(raw string) (string, error) {
	metadata := map[string]any{}
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
			return "", fmt.Errorf("decode session metadata: %w", err)
		}
	}
	metadata["title_renamed"] = true
	metadata["title_source"] = "manual"
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return "", fmt.Errorf("encode session metadata: %w", err)
	}
	return string(encoded), nil
}

func (s *Service) Archive(ctx context.Context, scope Scope, sessionID string) (*storage.Session, error) {
	session, err := s.targetSession(ctx, scope, sessionID)
	if err != nil {
		return nil, err
	}
	if session.ArchivedAt == nil {
		now := storage.Now()
		session.ArchivedAt = &now
		session.UpdatedAt = now
		if err := s.store.Sessions().Update(ctx, session); err != nil {
			return nil, err
		}
	}
	return session, nil
}

func (s *Service) Unarchive(ctx context.Context, scope Scope, sessionID string) (*storage.Session, error) {
	session, err := s.targetSession(ctx, scope, sessionID)
	if err != nil {
		return nil, err
	}
	if session.ArchivedAt != nil {
		session.ArchivedAt = nil
		session.UpdatedAt = storage.Now()
		if err := s.store.Sessions().Update(ctx, session); err != nil {
			return nil, err
		}
	}
	s.setCurrent(scope, session.ID)
	return session, nil
}

func (s *Service) Pin(ctx context.Context, scope Scope, sessionID string) (*storage.Session, error) {
	session, err := s.targetSession(ctx, scope, sessionID)
	if err != nil {
		return nil, err
	}
	if session.PinnedAt == nil {
		now := storage.Now()
		session.PinnedAt = &now
		session.UpdatedAt = now
		if err := s.store.Sessions().Update(ctx, session); err != nil {
			return nil, err
		}
	}
	return session, nil
}

func (s *Service) Unpin(ctx context.Context, scope Scope, sessionID string) (*storage.Session, error) {
	session, err := s.targetSession(ctx, scope, sessionID)
	if err != nil {
		return nil, err
	}
	if session.PinnedAt != nil {
		session.PinnedAt = nil
		session.UpdatedAt = storage.Now()
		if err := s.store.Sessions().Update(ctx, session); err != nil {
			return nil, err
		}
	}
	return session, nil
}

func (s *Service) Delete(ctx context.Context, scope Scope, sessionID string) error {
	session, err := s.targetSession(ctx, scope, sessionID)
	if err != nil {
		return err
	}
	if err := s.store.Sessions().Delete(ctx, session.ID); err != nil {
		return err
	}
	s.clearCurrentIf(scope, session.ID)
	return nil
}

func (s *Service) CleanupExpired(ctx context.Context, cutoff time.Time) (int, error) {
	return s.store.Sessions().DeleteExpired(ctx, cutoff)
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
