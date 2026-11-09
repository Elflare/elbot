package control

import (
	"context"
	"strings"

	"elbot/internal/workspace"
)

func (m *Supervisor) Workspace(ctx context.Context) (WorkspaceResult, error) {
	sessionID, state, err := m.currentWorkspace(ctx)
	if err != nil {
		return WorkspaceResult{}, err
	}
	return workspaceResult(sessionID, state), nil
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

func (m *Supervisor) WorkspaceFiles(ctx context.Context, req WorkspaceFilesRequest) (WorkspaceFilesResult, error) {
	_, state, err := m.currentWorkspace(ctx)
	if err != nil {
		return WorkspaceFilesResult{}, err
	}
	root, err := workspacePathForSide(state, req.Side)
	if err != nil {
		return WorkspaceFilesResult{}, err
	}
	if req.Path == "" {
		req.Path = "."
	}
	result, err := workspace.ListWorkspaceFiles(ctx, root, req.Path)
	if err != nil {
		return WorkspaceFilesResult{}, ClassifyError(err)
	}
	result.Side = req.Side
	return result, nil
}
func (m *Supervisor) SetUserWorkspace(ctx context.Context, req SetUserWorkspaceRequest) (WorkspaceResult, error) {
	if err := m.authorizeWorkspace(ctx); err != nil {
		return WorkspaceResult{}, err
	}
	path, err := workspace.ValidateWorkspaceDir(req.Path)
	if err != nil {
		return WorkspaceResult{}, invalid(err.Error())
	}
	sessionID, state, err := m.workspaces.SetUserDir(ctx, path)
	if err != nil {
		return WorkspaceResult{}, ClassifyError(err)
	}
	return workspaceResult(sessionID, state), nil
}

func (m *Supervisor) SetWorkspaceSync(ctx context.Context, req SetWorkspaceSyncRequest) (WorkspaceResult, error) {
	if err := m.authorizeWorkspace(ctx); err != nil {
		return WorkspaceResult{}, err
	}
	sessionID, state, err := m.workspaces.SetSync(ctx, req.AgentToUser, req.UserToAgent)
	if err != nil {
		return WorkspaceResult{}, ClassifyError(err)
	}
	return workspaceResult(sessionID, state), nil
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

func (m *Supervisor) authorizeWorkspace(ctx context.Context) error {
	return authorize(ctx, false, m.workspaces != nil && m.deps.Scope != nil)
}

func (m *Supervisor) currentWorkspace(ctx context.Context) (string, workspace.State, error) {
	if err := m.authorizeWorkspace(ctx); err != nil {
		return "", workspace.State{}, err
	}
	sessionID, state, err := m.workspaces.GetCurrentState(ctx)
	if err != nil {
		return "", workspace.State{}, ClassifyError(err)
	}
	return sessionID, state, nil
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
