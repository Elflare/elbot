// 职责：根据已准备的压缩结果创建派生会话。
// 继承来源归属并原子保存材料，仅为前台来源切换当前绑定。
package branch

import (
	"context"
	"fmt"

	"elbot/internal/session/share"
	"elbot/internal/storage"
)

type CompactedRequest struct {
	ID                   string              // 与源会话不同的预分配目标 ID。
	Title                string              // 压缩后标题。
	Metadata             string              // 上下文所有者准备的元数据 JSON。
	Seed                 *storage.NativeSeed // 协议组件准备的原生根；无原生材料时为 nil。
	ExpectedCheckpointID string              // 创建事务需要复核的源游标。
}

func (s *Service) CreateCompacted(ctx context.Context, scope share.Scope, sourceID string, req CompactedRequest) (*storage.Session, error) {
	if req.ID == "" || req.ID == sourceID {
		return nil, fmt.Errorf("a distinct preallocated session ID is required")
	}
	source, err := s.deps.Sessions.Get(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	background := share.IsBackground(source)
	var release func()
	if background {
		ctx, release, err = s.deps.Bindings.EnterSessions(ctx, sourceID, req.ID)
	} else {
		ctx, release, err = s.deps.Bindings.EnterActivation(ctx, scope, sourceID, req.ID)
	}
	if err != nil {
		return nil, err
	}
	defer release()
	source, err = s.deps.Sessions.Get(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	if background != share.IsBackground(source) {
		return nil, share.ErrForegroundSession
	}
	if background {
		// 后台压缩是原执行的交接，只匹配归属，不使用前台入口的访问豁免。
		if scope.ActorID != source.OwnerID || scope.Platform != source.Platform || scope.PlatformScopeID != source.PlatformScopeID {
			return nil, fmt.Errorf("session %s does not belong to current background execution", sourceID)
		}
	} else {
		if !share.CanAccess(scope, source) {
			return nil, fmt.Errorf("session %s is not in current platform scope", sourceID)
		}
		current := s.deps.Bindings.CurrentBinding(scope)
		if current == nil || current.SessionID() != sourceID {
			return nil, fmt.Errorf("session binding expired")
		}
		if original, ok := share.BindingFromContext(ctx); ok && original != current {
			return nil, fmt.Errorf("session binding expired")
		}
	}
	origin, _, err := share.Origin(source)
	if err != nil {
		return nil, err
	}
	fields, err := storage.DecodeSessionMetadata(req.Metadata)
	if err != nil {
		return nil, err
	}
	if err := share.InheritOrigin(origin, fields); err != nil {
		return nil, err
	}
	if err := fields.Set("title_renamed", true); err != nil {
		return nil, err
	}
	if err := fields.Set("title_source", "compact"); err != nil {
		return nil, err
	}
	metadata, err := fields.Encode()
	if err != nil {
		return nil, err
	}
	row := &storage.Session{ID: req.ID, OwnerID: source.OwnerID, Platform: source.Platform,
		PlatformScopeID: source.PlatformScopeID, Title: req.Title, Mode: source.Mode,
		Status: storage.SessionStatusActive, Metadata: metadata}
	if req.Seed == nil {
		err = s.deps.Sessions.Create(ctx, row)
	} else {
		_, err = s.deps.Sessions.CreateMaterial(ctx, storage.SessionMaterialCreate{Session: row, Seed: req.Seed,
			SourceSessionID: sourceID, ExpectedCheckpointID: req.ExpectedCheckpointID})
	}
	if err != nil {
		return nil, err
	}
	if !background {
		s.deps.Bindings.SetCurrent(ctx, scope, row.ID)
	}
	return row, nil
}
