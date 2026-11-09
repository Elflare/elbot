// 职责：持久化会话工作目录与目录通知状态。
// 通过绑定或 Session 准入协调并发更新，保留条件清除及后台初始化语义。
package control

import (
	"context"
	"slices"
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
	return slices.Contains(state.AgentNoticeDirs, strings.TrimSpace(dir)), err
}

func (s *Service) SetWorkspaceDirWithAgentNotice(ctx context.Context, id, dir string, mark bool) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil
	}
	return s.saveWorkspace(ctx, id, func(state *workspace.State) {
		state.Dir = dir
		if state.AgentToUser {
			state.UserDir = dir
		}
		if mark {
			markWorkspaceNotice(state, dir)
		}
	})
}

func (s *Service) ClearWorkspaceDirWithAgentNotice(ctx context.Context, id, dir string, mark bool) error {
	return s.saveWorkspace(ctx, id, func(state *workspace.State) {
		state.Dir = ""
		if state.AgentToUser {
			state.UserDir = ""
		}
		if mark {
			markWorkspaceNotice(state, strings.TrimSpace(dir))
		}
	})
}

func (s *Service) ClearWorkspaceDirIfMatches(ctx context.Context, id, dir string) (bool, error) {
	cleared := false
	err := s.saveWorkspace(ctx, id, func(state *workspace.State) {
		if state.Dir == dir {
			state.Dir = ""
			if state.AgentToUser {
				state.UserDir = ""
			}
			cleared = true
		}
	})
	return cleared && err == nil, err
}

func (s *Service) MarkWorkspaceAgentNoticeDir(ctx context.Context, id, dir string) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil
	}
	return s.saveWorkspace(ctx, id, func(state *workspace.State) { markWorkspaceNotice(state, dir) })
}

func (s *Service) EnsureWorkspaceDir(ctx context.Context, id, dir string) error {
	return s.saveWorkspace(ctx, id, func(state *workspace.State) {
		if state.Dir == "" {
			state.Dir = strings.TrimSpace(dir)
			if state.AgentToUser {
				state.UserDir = state.Dir
			}
		}
	})
}

func (s *Service) SetUserWorkspaceDir(ctx context.Context, id, dir string) error {
	return s.saveWorkspace(ctx, id, func(state *workspace.State) {
		state.UserDir = strings.TrimSpace(dir)
		if state.UserToAgent {
			state.Dir = state.UserDir
		}
	})
}

func (s *Service) SetWorkspaceSync(ctx context.Context, id string, agentToUser, userToAgent bool) (workspace.State, error) {
	var result workspace.State
	err := s.saveWorkspace(ctx, id, func(state *workspace.State) {
		newAgentToUser := agentToUser && !state.AgentToUser
		newUserToAgent := userToAgent && !state.UserToAgent
		state.AgentToUser, state.UserToAgent = agentToUser, userToAgent
		switch {
		case newUserToAgent:
			state.Dir = state.UserDir
		case newAgentToUser:
			state.UserDir = state.Dir
		}
		result = *state
	})
	return result, err
}

func markWorkspaceNotice(state *workspace.State, dir string) {
	if dir != "" && !slices.Contains(state.AgentNoticeDirs, dir) {
		state.AgentNoticeDirs = append(state.AgentNoticeDirs, dir)
	}
}

func (s *Service) saveWorkspace(ctx context.Context, id string, update func(*workspace.State)) error {
	ctx, release, err := s.enterSession(ctx, id)
	if err != nil {
		return err
	}
	defer release()
	_, err = s.deps.Sessions.Mutate(ctx, id, func(row *storage.Session) error {
		fields, err := storage.DecodeSessionMetadata(row.Metadata)
		if err != nil {
			return err
		}
		state, err := workspace.DecodeState(row.Metadata)
		if err != nil {
			return err
		}
		update(&state)
		if state.Dir == "" {
			delete(fields, "workspace_dir")
		} else if err := fields.Set("workspace_dir", state.Dir); err != nil {
			return err
		}
		if state.UserDir == "" {
			delete(fields, "user_workspace_dir")
		} else if err := fields.Set("user_workspace_dir", state.UserDir); err != nil {
			return err
		}
		if state.AgentToUser {
			if err := fields.Set("workspace_agent_to_user", true); err != nil {
				return err
			}
		} else {
			delete(fields, "workspace_agent_to_user")
		}
		if state.UserToAgent {
			if err := fields.Set("workspace_user_to_agent", true); err != nil {
				return err
			}
		} else {
			delete(fields, "workspace_user_to_agent")
		}
		if len(state.AgentNoticeDirs) == 0 {
			delete(fields, "workspace_agent_notice_dirs")
		} else if err := fields.Set("workspace_agent_notice_dirs", state.AgentNoticeDirs); err != nil {
			return err
		}
		encoded, err := fields.Encode()
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
