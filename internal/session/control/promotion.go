// 职责：实现已有会话恢复和后台会话的永久前台接管。
// 接管提交及绑定建立后，在释放准入前同步交付接管事实。
package control

import (
	"context"
	"elbot/internal/contextinfo"

	"elbot/internal/session/share"
	"elbot/internal/signal"
	"elbot/internal/storage"
)

type ForegroundActivatedEvent struct {
	Session storage.Session // 原子接管提交后的快照，不得修改其指针字段。
	Binding *share.Binding  // 新前台激活的原绑定。
}

type ForegroundOrigin struct {
	TaskType string                    `json:"task_type"` // 接管前任务类型。
	Owner    contextinfo.ActorIdentity `json:"owner"`     // 接管前的所有者。
	Platform string                    `json:"platform"`  // 接管前的平台。
	TaskID   string                    `json:"task_id"`   // 接管前任务原生 ID。
	Name     string                    `json:"name"`      // 接管前任务展示名。
}

func (s *Service) ObserveForegroundActivated(handler func(context.Context, ForegroundActivatedEvent) error, options signal.ConnectOptions) (*signal.Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, signal.ErrClosed
	}
	return s.foregroundActivated.Observe(handler, options)
}

func (s *Service) Resume(ctx context.Context, scope share.Scope, id string) (*storage.Session, error) {
	return s.activateExisting(ctx, scope, id, false)
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
		_, err = s.foregroundActivated.Emit(context.WithoutCancel(ctx), ForegroundActivatedEvent{Session: *row, Binding: s.CurrentBinding(scope)})
	}
	return row, err
}
