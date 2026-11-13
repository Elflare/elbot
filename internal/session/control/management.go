// 职责：实现改名、归档、取消归档、置顶及删除。
// 负责相应持久化变更，并在删除成功后使相关绑定失效。
package control

import (
	"context"
	"errors"
	"strings"

	"elbot/internal/operationresult"
	"elbot/internal/session/share"
	"elbot/internal/storage"
)

func (s *Service) Rename(ctx context.Context, scope share.Scope, id, title string) (operationresult.Result[*storage.Session], error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return operationresult.Result[*storage.Session]{Status: operationresult.InvalidArguments, Message: "title is required"}, nil
	}
	return s.mutateTarget(ctx, scope, id, func(row *storage.Session) error {
		fields, err := storage.DecodeSessionMetadata(row.Metadata)
		if err != nil {
			return err
		}
		if err := fields.Set("title_renamed", true); err != nil {
			return err
		}
		if err := fields.Set("title_source", "manual"); err != nil {
			return err
		}
		row.Metadata, err = fields.Encode()
		if err != nil {
			return err
		}
		row.Title, row.UpdatedAt = title, storage.Now()
		return nil
	})
}

func (s *Service) Archive(ctx context.Context, scope share.Scope, id string) (operationresult.Result[*storage.Session], error) {
	return s.mutateTarget(ctx, scope, id, func(row *storage.Session) error {
		if row.ArchivedAt == nil {
			now := storage.Now()
			row.ArchivedAt, row.UpdatedAt = &now, now
		}
		return nil
	})
}

func (s *Service) Unarchive(ctx context.Context, scope share.Scope, id string) (operationresult.Result[*storage.Session], error) {
	return s.activateExisting(ctx, scope, id, true)
}

func (s *Service) Pin(ctx context.Context, scope share.Scope, id string) (operationresult.Result[*storage.Session], error) {
	return s.mutateTarget(ctx, scope, id, func(row *storage.Session) error {
		if row.PinnedAt == nil {
			now := storage.Now()
			row.PinnedAt, row.UpdatedAt = &now, now
		}
		return nil
	})
}

func (s *Service) Unpin(ctx context.Context, scope share.Scope, id string) (operationresult.Result[*storage.Session], error) {
	return s.mutateTarget(ctx, scope, id, func(row *storage.Session) error {
		if row.PinnedAt != nil {
			row.PinnedAt, row.UpdatedAt = nil, storage.Now()
		}
		return nil
	})
}

func (s *Service) mutateTarget(ctx context.Context, scope share.Scope, id string, update func(*storage.Session) error) (operationresult.Result[*storage.Session], error) {
	entered, err := s.enterTarget(ctx, scope, id)
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	if entered.Status != operationresult.Success {
		return operationresult.Result[*storage.Session]{Status: entered.Status, Message: entered.Message}, nil
	}
	ctx, id, release := entered.Data.Context, entered.Data.ID, entered.Data.Release
	defer release()
	row, err := s.deps.Sessions.Get(ctx, id)
	if errors.Is(err, storage.ErrNotFound) {
		return operationresult.Result[*storage.Session]{Status: operationresult.NotFound, Message: "会话不存在"}, nil
	}
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	if !share.CanAccess(scope, row) {
		return operationresult.Result[*storage.Session]{Status: operationresult.Forbidden, Message: "无权访问该会话"}, nil
	}
	row, err = s.deps.Sessions.Mutate(ctx, id, update)
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	return operationresult.Result[*storage.Session]{Status: operationresult.Success, Data: row}, nil
}

func (s *Service) Delete(ctx context.Context, scope share.Scope, id string) (operationresult.Result[struct{}], error) {
	entered, err := s.enterTarget(ctx, scope, id)
	if err != nil {
		return operationresult.Result[struct{}]{}, err
	}
	if entered.Status != operationresult.Success {
		return operationresult.Result[struct{}]{Status: entered.Status, Message: entered.Message}, nil
	}
	ctx, id, release := entered.Data.Context, entered.Data.ID, entered.Data.Release
	defer release()
	row, err := s.deps.Sessions.Get(ctx, id)
	if errors.Is(err, storage.ErrNotFound) {
		return operationresult.Result[struct{}]{Status: operationresult.NotFound, Message: "会话不存在"}, nil
	}
	if err != nil {
		return operationresult.Result[struct{}]{}, err
	}
	if !share.CanAccess(scope, row) {
		return operationresult.Result[struct{}]{Status: operationresult.Forbidden, Message: "无权访问该会话"}, nil
	}
	if s.sessionBusy(id) {
		return operationresult.Result[struct{}]{Status: operationresult.Busy, Message: "会话正忙"}, nil
	}
	if err := s.deps.Sessions.Delete(ctx, id); err != nil {
		return operationresult.Result[struct{}]{}, err
	}
	s.clearSessionBindings(ctx, id)
	return operationresult.Result[struct{}]{Status: operationresult.Success}, nil
}
