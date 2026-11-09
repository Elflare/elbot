// 职责：持久化会话工作目录与目录通知状态。
// 通过绑定或 Session 准入协调并发更新，保留条件清除及后台初始化语义。
package control

import (
	"context"
	"strings"

	"elbot/internal/storage"
	"elbot/internal/workspace"
)

func (s *Service) workspaceState(ctx context.Context, id string) (workspace.State, error) {
	ctx, release, err := s.enterSession(ctx, id)
	if err != nil {
		return workspace.State{}, err
	}
	defer release()
	row, err := s.deps.Sessions.Get(ctx, id)
	if err != nil {
		return workspace.State{}, err
	}
	return workspace.DecodeState(row.Metadata)
}

func (s *Service) GetWorkspaceDir(ctx context.Context, id string) (string, error) {
	state, err := s.workspaceState(ctx, id)
	return state.Dir, err
}

func (s *Service) GetWorkspaceState(ctx context.Context, id string) (workspace.State, error) {
	return s.workspaceState(ctx, id)
}

func (s *Service) HasWorkspaceAgentNoticeDir(ctx context.Context, id, dir string) (bool, error) {
	state, err := s.workspaceState(ctx, id)
	return state.HasAgentNoticeDir(dir), err
}

func (s *Service) SetWorkspaceDirWithAgentNotice(ctx context.Context, id, dir string, mark bool) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil
	}
	return s.saveWorkspace(ctx, id, func(state *workspace.State) {
		state.SetAgentDir(dir)
		if mark {
			state.MarkAgentNoticeDir(dir)
		}
	})
}

func (s *Service) ClearWorkspaceDirWithAgentNotice(ctx context.Context, id, dir string, mark bool) error {
	return s.saveWorkspace(ctx, id, func(state *workspace.State) {
		state.ClearAgentDir(dir, mark)
	})
}

func (s *Service) ClearWorkspaceDirIfMatches(ctx context.Context, id, dir string) (bool, error) {
	cleared := false
	err := s.saveWorkspace(ctx, id, func(state *workspace.State) {
		cleared = state.ClearAgentDirIfMatches(dir)
	})
	return cleared && err == nil, err
}

func (s *Service) MarkWorkspaceAgentNoticeDir(ctx context.Context, id, dir string) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil
	}
	return s.saveWorkspace(ctx, id, func(state *workspace.State) { state.MarkAgentNoticeDir(dir) })
}

func (s *Service) EnsureWorkspaceDir(ctx context.Context, id, dir string) error {
	return s.saveWorkspace(ctx, id, func(state *workspace.State) { state.EnsureAgentDir(dir) })
}

func (s *Service) SetUserWorkspaceDir(ctx context.Context, id, dir string) error {
	return s.saveWorkspace(ctx, id, func(state *workspace.State) { state.SetUserDir(dir) })
}

func (s *Service) SetWorkspaceSync(ctx context.Context, id string, agentToUser, userToAgent bool, agentDir string) (workspace.State, error) {
	var result workspace.State
	err := s.saveWorkspace(ctx, id, func(state *workspace.State) {
		state.SetSync(agentToUser, userToAgent, agentDir)
		result = *state
	})
	return result, err
}

func (s *Service) saveWorkspace(ctx context.Context, id string, update func(*workspace.State)) error {
	ctx, release, err := s.enterSession(ctx, id)
	if err != nil {
		return err
	}
	defer release()
	_, err = s.deps.Sessions.Mutate(ctx, id, func(row *storage.Session) error {
		encoded, err := workspace.UpdateState(row.Metadata, update)
		if err != nil {
			return err
		}
		if row.Metadata != encoded {
			row.Metadata, row.UpdatedAt = encoded, storage.Now()
		}
		return nil
	})
	return err
}
