package control

import (
	"context"
	"strings"

	"elbot/internal/storage"
	"elbot/internal/workspace"
)

func (m *Supervisor) Workspace(ctx context.Context) (WorkspaceResult, error) {
	row, state, err := m.currentWorkspace(ctx)
	if err != nil {
		return WorkspaceResult{}, err
	}
	return workspaceResult(row.ID, state), nil
}

func (m *Supervisor) WorkspaceDirectories(ctx context.Context, req WorkspaceDirectoriesRequest) (WorkspaceDirectoriesResult, error) {
	_, state, err := m.currentWorkspace(ctx)
	if err != nil {
		return WorkspaceDirectoriesResult{}, err
	}
	path := strings.TrimSpace(req.Path)
	if path == "" {
		path = state.Dir
	}
	result, err := workspace.ListDirectories(ctx, path)
	return result, ClassifyError(err)
}

func (m *Supervisor) SetUserWorkspace(ctx context.Context, req SetUserWorkspaceRequest) (WorkspaceResult, error) {
	row, _, err := m.currentWorkspace(ctx)
	if err != nil {
		return WorkspaceResult{}, err
	}
	path, err := workspace.ValidateWorkspaceDir(req.Path)
	if err != nil {
		return WorkspaceResult{}, invalid(err.Error())
	}
	if err := m.deps.Sessions.SetUserWorkspaceDir(ctx, row.ID, path); err != nil {
		return WorkspaceResult{}, ClassifyError(err)
	}
	state, err := m.deps.Sessions.GetWorkspaceState(ctx, row.ID)
	if err != nil {
		return WorkspaceResult{}, ClassifyError(err)
	}
	return workspaceResult(row.ID, state), nil
}

func (m *Supervisor) SetWorkspaceSync(ctx context.Context, req SetWorkspaceSyncRequest) (WorkspaceResult, error) {
	row, _, err := m.currentWorkspace(ctx)
	if err != nil {
		return WorkspaceResult{}, err
	}
	state, err := m.deps.Sessions.SetWorkspaceSync(ctx, row.ID, req.AgentToUser, req.UserToAgent)
	if err != nil {
		return WorkspaceResult{}, ClassifyError(err)
	}
	return workspaceResult(row.ID, state), nil
}

func (m *Supervisor) WorkspaceGit(ctx context.Context, req WorkspaceGitRequest) (WorkspaceGitResult, error) {
	_, state, err := m.currentWorkspace(ctx)
	if err != nil {
		return WorkspaceGitResult{}, err
	}
	path, err := workspacePathForSide(state, req.Side)
	if err != nil {
		return WorkspaceGitResult{}, err
	}
	if path == "" {
		return WorkspaceGitResult{Changes: []workspace.GitChange{}}, nil
	}
	result, err := workspace.ReadGitStatus(ctx, path)
	return result, ClassifyError(err)
}

func (m *Supervisor) WorkspaceGitDiff(ctx context.Context, req WorkspaceGitRequest) (string, error) {
	_, state, err := m.currentWorkspace(ctx)
	if err != nil {
		return "", err
	}
	path, err := workspacePathForSide(state, req.Side)
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", invalid("workspace 尚未设置")
	}
	diff, err := workspace.ReadGitDiff(ctx, path, req.Path)
	return diff, ClassifyError(err)
}

func (m *Supervisor) currentWorkspace(ctx context.Context) (*storage.Session, workspace.State, error) {
	if err := authorize(ctx, false, m.deps.Sessions != nil && m.deps.Scope != nil); err != nil {
		return nil, workspace.State{}, err
	}
	row, err := m.deps.Sessions.Current(ctx, m.deps.Scope(ctx))
	if err != nil {
		return nil, workspace.State{}, ClassifyError(err)
	}
	state, err := m.deps.Sessions.GetWorkspaceState(ctx, row.ID)
	if err != nil {
		return nil, workspace.State{}, ClassifyError(err)
	}
	return row, state, nil
}

func workspaceResult(sessionID string, state workspace.State) WorkspaceResult {
	return WorkspaceResult{SessionID: sessionID, AgentPath: state.Dir, UserPath: state.UserDir, AgentToUser: state.AgentToUser, UserToAgent: state.UserToAgent}
}

func workspacePathForSide(state workspace.State, side string) (string, error) {
	switch side {
	case "agent":
		return state.Dir, nil
	case "user":
		return state.UserDir, nil
	default:
		return "", invalid("side 必须是 user 或 agent")
	}
}
