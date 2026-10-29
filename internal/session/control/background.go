// 职责：准备后台会话并查询前台接管事实。
// 后台准备不激活 current，负责后台身份元数据及初始标题。
package control

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"elbot/internal/session/share"
	"elbot/internal/storage"
)

func (s *Service) PrepareBackground(ctx context.Context, scope share.Scope, req BackgroundRequest) (*storage.Session, error) {
	if req.SessionID != "" {
		ctx, release, err := s.EnterSessions(ctx, req.SessionID)
		if err != nil {
			return nil, err
		}
		defer release()
		return s.deps.Sessions.Mutate(ctx, req.SessionID, func(row *storage.Session) error {
			if share.WasPromoted(row) {
				return share.ErrForegroundSession
			}
			if !share.IsBackground(row) {
				return fmt.Errorf("session is not a background session")
			}
			metadata, err := backgroundMetadata(row.Metadata, req)
			if err != nil {
				return err
			}
			row.Metadata, row.UpdatedAt = metadata, storage.Now()
			return nil
		})
	}
	id := storage.NewID()
	ctx, release, err := s.EnterSessions(ctx, id)
	if err != nil {
		return nil, err
	}
	defer release()
	metadata, err := backgroundMetadata("", req)
	if err != nil {
		return nil, err
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = share.BackgroundTitle(req.Kind, req.Name)
	}
	row := &storage.Session{ID: id, OwnerID: scope.ActorID, Platform: scope.Platform, PlatformScopeID: scope.PlatformScopeID,
		Mode: storage.SessionModeBackground, Status: storage.SessionStatusActive, Title: title, Metadata: metadata}
	if err := s.deps.Sessions.Create(ctx, row); err != nil {
		return nil, err
	}
	return row, nil
}

func (s *Service) SessionTakenOver(ctx context.Context, id string) (bool, error) {
	if id == "" {
		return false, nil
	}
	row, err := s.deps.Sessions.Get(ctx, id)
	if errors.Is(err, storage.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return share.WasPromoted(row), nil
}

func backgroundMetadata(raw string, req BackgroundRequest) (string, error) {
	fields, err := storage.DecodeSessionMetadata(raw)
	if err != nil {
		return "", err
	}
	for key, value := range req.Metadata {
		if strings.TrimSpace(key) != "" {
			if err := fields.Set(key, value); err != nil {
				return "", err
			}
		}
	}
	if err := share.BackgroundMetadata(fields, req.Kind, req.Name); err != nil {
		return "", err
	}
	return fields.Encode()
}
