package control

import (
	"context"
	"errors"
	"strings"

	"elbot/internal/control/share"
	"elbot/internal/operationresult"
	"elbot/internal/workspace"
)

func (m *Supervisor) Workspace(ctx context.Context) (operationresult.Result[WorkspaceResult], error) {
	current, err := m.currentWorkspace(ctx)
	if err != nil {
		return operationresult.Result[WorkspaceResult]{}, err
	}
	if current.Status != operationresult.Success {
		return operationresult.Result[WorkspaceResult]{Status: current.Status, Message: current.Message}, nil
	}
	return operationresult.Result[WorkspaceResult]{Status: operationresult.Success, Data: workspaceResult(current.Data.SessionID, current.Data.State)}, nil
}

func (m *Supervisor) WorkspaceDirectories(ctx context.Context, req WorkspaceDirectoriesRequest) (operationresult.Result[WorkspaceDirectoriesResult], error) {
	current, err := m.currentWorkspace(ctx)
	if err != nil {
		return operationresult.Result[WorkspaceDirectoriesResult]{}, err
	}
	if current.Status != operationresult.Success {
		return operationresult.Result[WorkspaceDirectoriesResult]{Status: current.Status, Message: current.Message}, nil
	}
	state := current.Data.State
	path := strings.TrimSpace(req.Path)
	if path == "" {
		path = state.Dir
	}
	result, err := workspace.ListDirectories(ctx, path)
	return result, err
}

func (m *Supervisor) WorkspaceFiles(ctx context.Context, req WorkspaceFilesRequest) (operationresult.Result[WorkspaceFilesResult], error) {
	current, err := m.currentWorkspace(ctx)
	if err != nil {
		return operationresult.Result[WorkspaceFilesResult]{}, err
	}
	if current.Status != operationresult.Success {
		return operationresult.Result[WorkspaceFilesResult]{Status: current.Status, Message: current.Message}, nil
	}
	state := current.Data.State
	root, err := workspacePathForSide(state, req.Side)
	if err != nil {
		return operationresult.Result[WorkspaceFilesResult]{Status: operationresult.InvalidArguments, Message: err.Error()}, nil
	}
	if req.Path == "" {
		req.Path = "."
	}
	result, err := workspace.ListWorkspaceFiles(ctx, root, req.Path)
	if err != nil {
		return operationresult.Result[WorkspaceFilesResult]{}, err
	}
	if result.Status != operationresult.Success {
		return operationresult.Result[WorkspaceFilesResult]{Status: result.Status, Message: result.Message}, nil
	}
	result.Data.Side = req.Side
	return operationresult.Result[WorkspaceFilesResult]{Status: operationresult.Success, Data: result.Data}, nil
}
func (m *Supervisor) SetUserWorkspace(ctx context.Context, req SetUserWorkspaceRequest) (operationresult.Result[WorkspaceResult], error) {
	authorized, err := share.Authorize(ctx, false)
	if err != nil {
		return operationresult.Result[WorkspaceResult]{}, err
	}
	if authorized.Status != operationresult.Success {
		return operationresult.Result[WorkspaceResult]{Status: authorized.Status, Message: authorized.Message}, nil
	}
	path, err := workspace.ValidateWorkspaceDir(req.Path)
	if err != nil {
		return operationresult.Result[WorkspaceResult]{}, err
	}
	if path.Status != operationresult.Success {
		return operationresult.Result[WorkspaceResult]{Status: path.Status, Message: path.Message}, nil
	}
	result, err := m.workspaces.SetUserDir(ctx, path.Data)
	if err != nil {
		return operationresult.Result[WorkspaceResult]{}, err
	}
	if result.Status != operationresult.Success {
		return operationresult.Result[WorkspaceResult]{Status: result.Status, Message: result.Message}, nil
	}
	return operationresult.Result[WorkspaceResult]{Status: operationresult.Success, Data: workspaceResult(result.Data.SessionID, result.Data.State)}, nil
}

func (m *Supervisor) SetWorkspaceSync(ctx context.Context, req SetWorkspaceSyncRequest) (operationresult.Result[WorkspaceResult], error) {
	authorized, err := share.Authorize(ctx, false)
	if err != nil {
		return operationresult.Result[WorkspaceResult]{}, err
	}
	if authorized.Status != operationresult.Success {
		return operationresult.Result[WorkspaceResult]{Status: authorized.Status, Message: authorized.Message}, nil
	}
	result, err := m.workspaces.SetSync(ctx, req.AgentToUser, req.UserToAgent)
	if err != nil {
		return operationresult.Result[WorkspaceResult]{}, err
	}
	if result.Status != operationresult.Success {
		return operationresult.Result[WorkspaceResult]{Status: result.Status, Message: result.Message}, nil
	}
	return operationresult.Result[WorkspaceResult]{Status: operationresult.Success, Data: workspaceResult(result.Data.SessionID, result.Data.State)}, nil
}

func (m *Supervisor) WorkspaceGit(ctx context.Context, req WorkspaceGitRequest) (operationresult.Result[WorkspaceGitResult], error) {
	current, err := m.currentWorkspace(ctx)
	if err != nil {
		return operationresult.Result[WorkspaceGitResult]{}, err
	}
	if current.Status != operationresult.Success {
		return operationresult.Result[WorkspaceGitResult]{Status: current.Status, Message: current.Message}, nil
	}
	state := current.Data.State
	path, err := workspacePathForSide(state, req.Side)
	if err != nil {
		return operationresult.Result[WorkspaceGitResult]{Status: operationresult.InvalidArguments, Message: err.Error()}, nil
	}
	if path == "" {
		return operationresult.Result[WorkspaceGitResult]{Status: operationresult.Success, Data: WorkspaceGitResult{Changes: []workspace.GitChange{}}}, nil
	}
	result, err := workspace.ReadGitStatus(ctx, path)
	return result, err
}

func (m *Supervisor) WorkspaceGitDiff(ctx context.Context, req WorkspaceGitRequest) (operationresult.Result[string], error) {
	current, err := m.currentWorkspace(ctx)
	if err != nil {
		return operationresult.Result[string]{}, err
	}
	if current.Status != operationresult.Success {
		return operationresult.Result[string]{Status: current.Status, Message: current.Message}, nil
	}
	state := current.Data.State
	path, err := workspacePathForSide(state, req.Side)
	if err != nil {
		return operationresult.Result[string]{Status: operationresult.InvalidArguments, Message: err.Error()}, nil
	}
	if path == "" {
		return operationresult.Result[string]{Status: operationresult.Unavailable, Message: "workspace 尚未设置"}, nil
	}
	diff, err := workspace.ReadGitDiff(ctx, path, req.Path)
	return diff, err
}

func (m *Supervisor) currentWorkspace(ctx context.Context) (operationresult.Result[workspace.CurrentState], error) {
	authorized, err := share.Authorize(ctx, false)
	if err != nil {
		return operationresult.Result[workspace.CurrentState]{}, err
	}
	if authorized.Status != operationresult.Success {
		return operationresult.Result[workspace.CurrentState]{Status: authorized.Status, Message: authorized.Message}, nil
	}
	result, err := m.workspaces.GetCurrentState(ctx)
	if err != nil {
		return operationresult.Result[workspace.CurrentState]{}, err
	}
	if result.Status != operationresult.Success {
		return operationresult.Result[workspace.CurrentState]{Status: result.Status, Message: result.Message}, nil
	}
	return result, nil
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
		return "", errors.New("side 必须是 user 或 agent")
	}
}
