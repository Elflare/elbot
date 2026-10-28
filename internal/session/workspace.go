// 职责：提供会话工作目录存储适配的公开入口。
// 持久化与并发更新委托 control，不向调用方交付内部组件或存储仓库。
package session

import (
	"context"

	"elbot/internal/session/control"
	"elbot/internal/workspace"
)

// WorkspaceStore 将工作目录操作绑定到一个会话，不持有可变行快照或仓库。
type WorkspaceStore struct {
	control   *control.Service // 持久化及并发协调的唯一所有者。
	sessionID string           // 适配器固定服务的会话。
}

var (
	_ workspace.WorkspaceStore            = (*WorkspaceStore)(nil)
	_ workspace.WorkspaceAgentNoticeStore = (*WorkspaceStore)(nil)
)

func (s *Supervisor) WorkspaceStore(sessionID string) *WorkspaceStore {
	return &WorkspaceStore{control: s.control, sessionID: sessionID}
}

func (s *WorkspaceStore) GetWorkspaceDir(ctx context.Context) (string, error) {
	return s.control.GetWorkspaceDir(ctx, s.sessionID)
}

func (s *WorkspaceStore) SetWorkspaceDir(ctx context.Context, dir string) error {
	return s.control.SetWorkspaceDirWithAgentNotice(ctx, s.sessionID, dir, false)
}

func (s *WorkspaceStore) ClearWorkspaceDir(ctx context.Context) error {
	return s.control.ClearWorkspaceDirWithAgentNotice(ctx, s.sessionID, "", false)
}

func (s *WorkspaceStore) ClearWorkspaceDirIfMatches(ctx context.Context, dir string) (bool, error) {
	return s.control.ClearWorkspaceDirIfMatches(ctx, s.sessionID, dir)
}

func (s *WorkspaceStore) HasWorkspaceAgentNoticeDir(ctx context.Context, dir string) (bool, error) {
	return s.control.HasWorkspaceAgentNoticeDir(ctx, s.sessionID, dir)
}

func (s *WorkspaceStore) MarkWorkspaceAgentNoticeDir(ctx context.Context, dir string) error {
	return s.control.MarkWorkspaceAgentNoticeDir(ctx, s.sessionID, dir)
}

func (s *WorkspaceStore) SetWorkspaceDirWithAgentNotice(ctx context.Context, dir string, mark bool) error {
	return s.control.SetWorkspaceDirWithAgentNotice(ctx, s.sessionID, dir, mark)
}

func (s *WorkspaceStore) ClearWorkspaceDirWithAgentNotice(ctx context.Context, dir string, mark bool) error {
	return s.control.ClearWorkspaceDirWithAgentNotice(ctx, s.sessionID, dir, mark)
}

func (s *WorkspaceStore) EnsureWorkspaceDir(ctx context.Context, dir string) error {
	return s.control.EnsureWorkspaceDir(ctx, s.sessionID, dir)
}
