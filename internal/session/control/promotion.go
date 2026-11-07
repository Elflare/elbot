// 职责：实现已有会话恢复和后台会话的永久前台接管。
// 接管提交及绑定建立后，在释放准入前同步交付接管事实。
package control

import (
	"context"
	"elbot/internal/contextinfo"
	"fmt"

	"elbot/internal/session/share"
	"elbot/internal/storage"
)

type ForegroundOrigin struct {
	TaskType string                    `json:"task_type"` // 接管前任务类型。
	Owner    contextinfo.ActorIdentity `json:"owner"`     // 接管前的所有者。
	Platform string                    `json:"platform"`  // 接管前的平台。
	TaskID   string                    `json:"task_id"`   // 接管前任务原生 ID。
	Name     string                    `json:"name"`      // 接管前任务展示名。
}

func (s *Service) Resume(ctx context.Context, scope share.Scope, id string) (*storage.Session, error) {
	return s.activateExisting(ctx, scope, id, false)
}

func (s *Service) ResumeLast(ctx context.Context, scope share.Scope) (*storage.Session, error) {
	ctx, release, err := s.deps.Bindings.EnterScope(ctx, scope)
	if err != nil {
		return nil, err
	}
	defer release()
	query := storage.ListSessionsRequest{
		Owner: scope.Owner, Platform: scope.Platform, ChatType: scope.ChatType, ChatID: scope.ChatID,
		IncludeAllPlatforms: scope.IsCLI, IncludeSamePlatformBackground: !scope.IsCLI && scope.ChatType == contextinfo.ChatPrivate,
		RecentFirst: true, Limit: 1,
	}
	if binding := s.CurrentBinding(scope); binding != nil {
		query.ExcludeSessionID = binding.SessionID()
	}
	rows, err := s.deps.Sessions.List(ctx, query)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("没有其他可恢复会话: %w", storage.ErrNotFound)
	}
	return s.activateExisting(ctx, scope, rows[0].ID, false)
}

func (s *Service) activateExisting(ctx context.Context, scope share.Scope, id string, unarchive bool) (*storage.Session, error) {
	ctx, id, release, err := s.enterTarget(ctx, scope, id)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := s.canReplaceCurrent(scope, id); err != nil {
		return nil, err
	}
	promoted := false
	row, err := s.deps.Sessions.Mutate(ctx, id, func(row *storage.Session) error {
		if !share.IsBackground(row) || !s.deps.IsSuperadmin(scope) {
			if err := checkAccess(scope, row); err != nil {
				return err
			}
		}
		if share.IsBackground(row) {
			if err := s.deps.CheckForeground(ctx, row); err != nil {
				return err
			}
			fields, err := storage.DecodeSessionMetadata(row.Metadata)
			if err != nil {
				return err
			}
			if err := fields.Set("foreground_origin", ForegroundOrigin{TaskType: row.TaskType, TaskID: row.TaskID, Name: row.TaskName, Owner: row.Owner, Platform: row.Platform}); err != nil {
				return err
			}
			row.Metadata, err = fields.Encode()
			if err != nil {
				return err
			}
			row.Owner, row.Platform, row.ChatType, row.ChatID = scope.Owner, scope.Platform, scope.ChatType, scope.ChatID
			row.TaskType, row.TaskID, row.TaskName = "", "", ""
			row.Mode = storage.SessionModeWork
			promoted = true
		}
		if unarchive {
			row.ArchivedAt = nil
		}
		row.UpdatedAt = storage.Now()
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.SetCurrent(ctx, scope, id)
	if promoted {
		s.deps.AdoptForeground(context.WithoutCancel(ctx), row, s.CurrentBinding(scope))
	}
	return row, err
}
