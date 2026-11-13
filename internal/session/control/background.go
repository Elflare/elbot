// 职责：准备后台会话并查询前台接管事实。
// 后台准备不激活 current，负责后台身份元数据及初始标题。
package control

import (
	"context"
	"errors"
	"strings"

	"elbot/internal/operationresult"
	"elbot/internal/session/share"
	"elbot/internal/storage"
)

func (s *Service) PrepareBackground(ctx context.Context, scope share.Scope, req BackgroundRequest) (operationresult.Result[*storage.Session], error) {
	if req.SessionID != "" {
		ctx, release, err := s.EnterSessions(ctx, req.SessionID)
		if err != nil {
			return operationresult.Result[*storage.Session]{}, err
		}
		defer release()
		row, err := s.deps.Sessions.Get(ctx, req.SessionID)
		if errors.Is(err, storage.ErrNotFound) {
			return operationresult.Result[*storage.Session]{Status: operationresult.NotFound, Message: "后台会话不存在"}, nil
		}
		if err != nil {
			return operationresult.Result[*storage.Session]{}, err
		}
		if !share.IsBackground(row) {
			return operationresult.Result[*storage.Session]{Status: operationresult.Conflict, Message: "session is not a background session"}, nil
		}
		metadata, err := backgroundMetadata(row.Metadata, req, nil)
		if err != nil {
			return operationresult.Result[*storage.Session]{}, err
		}
		updated, err := s.deps.Sessions.Mutate(ctx, req.SessionID, func(row *storage.Session) error {
			row.Metadata, row.UpdatedAt = metadata, storage.Now()
			return nil
		})
		if err != nil {
			return operationresult.Result[*storage.Session]{}, err
		}
		return operationresult.Result[*storage.Session]{Status: operationresult.Success, Data: updated}, nil
	}
	origin := share.BackgroundIDOrigin(scope, req.TaskType, req.TargetPlatforms)
	id, err := s.deps.AllocateID(ctx, origin)
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	ctx, release, err := s.EnterSessions(ctx, id)
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	defer release()
	metadata, err := backgroundMetadata("", req, &origin)
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	title := strings.TrimSpace(req.Name)
	if title == "" {
		title = share.BackgroundTitle(req.TaskType, req.TaskID)
	}
	row := &storage.Session{ID: id, Owner: scope.Owner, Platform: scope.Platform, TaskType: req.TaskType, TaskID: req.TaskID, TaskName: req.Name,
		Mode: storage.SessionModeBackground, Status: storage.SessionStatusActive, Title: title, Metadata: metadata}
	if err := s.deps.Sessions.Create(ctx, row); err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	return operationresult.Result[*storage.Session]{Status: operationresult.Success, Data: row}, nil
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

func backgroundMetadata(raw string, req BackgroundRequest, origin *share.IDOrigin) (string, error) {
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
	if err := share.BackgroundMetadata(fields, req.TaskType); err != nil {
		return "", err
	}
	if origin != nil {
		if err := origin.Save(fields); err != nil {
			return "", err
		}
	}
	return fields.Encode()
}
