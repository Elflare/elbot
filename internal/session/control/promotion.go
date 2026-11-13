// 职责：实现已有会话恢复和后台会话的永久前台接管。
// 接管提交及绑定建立后，在释放准入前同步交付接管事实。
package control

import (
	"context"
	"errors"

	"elbot/internal/contextinfo"
	"elbot/internal/operationresult"
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

func (s *Service) Resume(ctx context.Context, scope share.Scope, id string) (operationresult.Result[*storage.Session], error) {
	return s.activateExisting(ctx, scope, id, false)
}

func (s *Service) ResumeLast(ctx context.Context, scope share.Scope, allPlatforms bool) (operationresult.Result[*storage.Session], error) {
	found, err := s.FindLast(ctx, scope, allPlatforms)
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	if found.Status != operationresult.Success {
		return operationresult.Result[*storage.Session]{Status: found.Status, Message: found.Message}, nil
	}
	return s.activateExisting(ctx, scope, found.Data, false)
}

func (s *Service) FindLast(ctx context.Context, scope share.Scope, allPlatforms bool) (operationresult.Result[string], error) {
	ctx, release, err := s.deps.Bindings.EnterScope(ctx, scope)
	if err != nil {
		return operationresult.Result[string]{}, err
	}
	defer release()
	query := storage.ListSessionsRequest{
		Owner: scope.Owner, Platform: scope.Platform, ChatType: scope.ChatType, ChatID: scope.ChatID,
		IncludeAllPlatforms: allPlatforms, IncludeSamePlatformBackground: !allPlatforms && scope.ChatType == contextinfo.ChatPrivate,
		RecentFirst: true, Limit: 1,
	}
	if binding := s.CurrentBinding(scope); binding != nil {
		query.ExcludeSessionID = binding.SessionID()
	}
	rows, err := s.deps.Sessions.List(ctx, query)
	if err != nil {
		return operationresult.Result[string]{}, err
	}
	if len(rows) == 0 {
		return operationresult.Result[string]{Status: operationresult.NotFound, Message: "没有其他可恢复会话"}, nil
	}
	return operationresult.Result[string]{Status: operationresult.Success, Data: rows[0].ID}, nil
}

func (s *Service) activateExisting(ctx context.Context, scope share.Scope, id string, unarchive bool) (operationresult.Result[*storage.Session], error) {
	entered, err := s.enterTarget(ctx, scope, id)
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	if entered.Status != operationresult.Success {
		return operationresult.Result[*storage.Session]{Status: entered.Status, Message: entered.Message}, nil
	}
	ctx, id, release := entered.Data.Context, entered.Data.ID, entered.Data.Release
	defer release()
	if s.currentBusy(scope, id) {
		return operationresult.Result[*storage.Session]{Status: operationresult.Busy, Message: "会话正忙"}, nil
	}
	current, err := s.deps.Sessions.Get(ctx, id)
	if errors.Is(err, storage.ErrNotFound) {
		return operationresult.Result[*storage.Session]{Status: operationresult.NotFound, Message: "会话不存在"}, nil
	}
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	if (!share.IsBackground(current) || !s.deps.IsSuperadmin(scope)) && !share.CanAccess(scope, current) {
		return operationresult.Result[*storage.Session]{Status: operationresult.Forbidden, Message: "无权访问该会话"}, nil
	}
	rejected := operationresult.Result[struct{}]{Status: operationresult.Success}
	promoted := false
	row, err := s.deps.Sessions.Mutate(ctx, id, func(row *storage.Session) error {
		if share.IsBackground(row) {
			result, err := s.deps.CheckForeground(ctx, row)
			if err != nil {
				return err
			}
			if result.Status != operationresult.Success {
				rejected = result
				return nil
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
		return operationresult.Result[*storage.Session]{}, err
	}
	if rejected.Status != operationresult.Success {
		return operationresult.Result[*storage.Session]{Status: rejected.Status, Message: rejected.Message}, nil
	}
	s.SetCurrent(ctx, scope, id)
	if promoted {
		s.deps.AdoptForeground(context.WithoutCancel(ctx), row, s.CurrentBinding(scope))
	}
	return operationresult.Result[*storage.Session]{Status: operationresult.Success, Data: row}, nil
}
