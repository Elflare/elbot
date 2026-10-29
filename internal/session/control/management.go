// 职责：实现改名、归档、取消归档、置顶及删除。
// 负责相应持久化变更，并在删除成功后使相关绑定失效。
package control

import (
	"context"
	"fmt"
	"strings"

	"elbot/internal/session/share"
	"elbot/internal/storage"
)

func (s *Service) Rename(ctx context.Context, scope share.Scope, id, title string) (*storage.Session, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, fmt.Errorf("title is required")
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

func (s *Service) Archive(ctx context.Context, scope share.Scope, id string) (*storage.Session, error) {
	return s.mutateTarget(ctx, scope, id, func(row *storage.Session) error {
		if row.ArchivedAt == nil {
			now := storage.Now()
			row.ArchivedAt, row.UpdatedAt = &now, now
		}
		return nil
	})
}

func (s *Service) Unarchive(ctx context.Context, scope share.Scope, id string) (*storage.Session, error) {
	return s.activateExisting(ctx, scope, id, true)
}

func (s *Service) Pin(ctx context.Context, scope share.Scope, id string) (*storage.Session, error) {
	return s.mutateTarget(ctx, scope, id, func(row *storage.Session) error {
		if row.PinnedAt == nil {
			now := storage.Now()
			row.PinnedAt, row.UpdatedAt = &now, now
		}
		return nil
	})
}

func (s *Service) Unpin(ctx context.Context, scope share.Scope, id string) (*storage.Session, error) {
	return s.mutateTarget(ctx, scope, id, func(row *storage.Session) error {
		if row.PinnedAt != nil {
			row.PinnedAt, row.UpdatedAt = nil, storage.Now()
		}
		return nil
	})
}

func (s *Service) mutateTarget(ctx context.Context, scope share.Scope, id string, update func(*storage.Session) error) (*storage.Session, error) {
	ctx, id, release, err := s.enterTarget(ctx, scope, id)
	if err != nil {
		return nil, err
	}
	defer release()
	return s.deps.Sessions.Mutate(ctx, id, func(row *storage.Session) error {
		if err := checkAccess(scope, row); err != nil {
			return err
		}
		return update(row)
	})
}

func (s *Service) Delete(ctx context.Context, scope share.Scope, id string) error {
	ctx, id, release, err := s.enterTarget(ctx, scope, id)
	if err != nil {
		return err
	}
	defer release()
	row, err := s.deps.Sessions.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := checkAccess(scope, row); err != nil {
		return err
	}
	if err := s.requireIdle(id); err != nil {
		return err
	}
	if err := s.deps.Sessions.Delete(ctx, id); err != nil {
		return err
	}
	s.clearSessionBindings(ctx, id)
	return nil
}
