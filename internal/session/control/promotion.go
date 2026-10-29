// 职责：实现已有会话恢复和后台会话的永久前台接管。
// 接管提交及绑定建立后，在释放准入前同步交付接管事实。
package control

import (
	"context"
	"encoding/json"

	"elbot/internal/session/share"
	"elbot/internal/signal"
	"elbot/internal/storage"
)

type ForegroundActivatedEvent struct {
	Session storage.Session // 原子接管提交后的快照，不得修改其指针字段。
	Binding *share.Binding  // 新前台激活的原绑定。
}

type ForegroundOrigin struct {
	Kind     string `json:"kind"`     // 接管前的后台来源。
	OwnerID  string `json:"owner_id"` // 接管前的所有者。
	Platform string `json:"platform"` // 接管前的平台。
	ScopeID  string `json:"scope_id"` // 接管前的任务范围。
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
		if err := checkAccess(scope, row); err != nil {
			return err
		}
		if share.IsBackground(row) {
			if err := s.deps.CheckForeground(ctx, row); err != nil {
				return err
			}
			fields, err := storage.DecodeSessionMetadata(row.Metadata)
			if err != nil {
				return err
			}
			var kind string
			if raw, ok := fields["background_kind"]; ok {
				if err := json.Unmarshal(raw, &kind); err != nil {
					return err
				}
			}
			if err := fields.Set("foreground_origin", ForegroundOrigin{Kind: kind, OwnerID: row.OwnerID, Platform: row.Platform, ScopeID: row.PlatformScopeID}); err != nil {
				return err
			}
			delete(fields, "background_kind")
			row.Metadata, err = fields.Encode()
			if err != nil {
				return err
			}
			row.OwnerID, row.Platform, row.PlatformScopeID = scope.ActorID, scope.Platform, scope.PlatformScopeID
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
