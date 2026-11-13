package management

import (
	"context"
	"elbot/internal/control"
	"elbot/internal/fileops"
	"elbot/internal/operationresult"
	"elbot/internal/tool"
)

// ControlCapabilities is the business surface consumed by the management API.
type ControlCapabilities interface {
	ListCommands(context.Context, control.ListCommandsRequest) (operationresult.Result[control.ListCommandsResult], error)
	Status(context.Context) (operationresult.Result[control.StatusResult], error)
	ListSessions(context.Context, control.ListSessionsRequest) (operationresult.Result[control.ListSessionsResult], error)
	GetSession(context.Context, control.GetSessionRequest) (operationresult.Result[control.GetSessionResult], error)
	NewSession(context.Context) (operationresult.Result[control.NewSessionResult], error)
	RenameSession(context.Context, control.RenameSessionRequest) (operationresult.Result[control.RenameSessionResult], error)
	SetSessionArchived(context.Context, control.SetSessionArchivedRequest) (operationresult.Result[control.SetSessionArchivedResult], error)
	SetSessionPinned(context.Context, control.SetSessionPinnedRequest) (operationresult.Result[control.SetSessionPinnedResult], error)
	DeleteSession(context.Context, control.DeleteSessionRequest) (operationresult.Result[control.DeleteSessionResult], error)
	CleanSessions(context.Context, control.CleanSessionsRequest) (operationresult.Result[control.CleanSessionsResult], error)
	ResumeSession(context.Context, control.ResumeSessionRequest) (operationresult.Result[control.ResumeSessionResult], error)
	ForkSession(context.Context, control.ForkSessionRequest) (operationresult.Result[control.ForkSessionResult], error)
	CompactSession(context.Context, control.CompactSessionRequest) (operationresult.Result[control.CompactSessionResult], error)
	SetMode(context.Context, control.SetModeRequest) (operationresult.Result[control.SetModeResult], error)
	SessionMessages(context.Context, control.SessionMessagesRequest) (operationresult.Result[control.SessionMessagesResult], error)
	ListModels(context.Context, control.ListModelsRequest) (operationresult.Result[control.ListModelsResult], error)
	RefreshModels(context.Context, control.ListModelsRequest) (operationresult.Result[control.RefreshModelsResult], error)
	SelectModel(context.Context, control.SelectModelRequest) (operationresult.Result[control.SelectModelResult], error)
	ListRequests(context.Context) (operationresult.Result[control.ListRequestsResult], error)
	StopRequest(context.Context, control.StopRequestRequest) (operationresult.Result[control.StopRequestResult], error)
	StopSession(context.Context) (operationresult.Result[control.StopSessionResult], error)
	StopAllRequests(context.Context) (operationresult.Result[control.StopAllRequestsResult], error)
	QueryLogs(context.Context, control.QueryLogsRequest) (operationresult.Result[control.QueryLogsResult], error)
	QueryUsage(context.Context, control.QueryUsageRequest) (operationresult.Result[control.QueryUsageResult], error)
	CheckConfiguration(context.Context) (operationresult.Result[control.CheckConfigurationResult], error)
	ListHooks(context.Context) (operationresult.Result[control.ListHooksResult], error)
	GetHook(context.Context, control.GetHookRequest) (operationresult.Result[control.GetHookResult], error)
	ReloadHooks(context.Context) (operationresult.Result[control.ReloadHooksResult], error)
	StartHook(context.Context, control.StartHookRequest) (operationresult.Result[struct{}], error)
	StopHook(context.Context, control.StopHookRequest) (operationresult.Result[struct{}], error)
	RestartHook(context.Context, control.RestartHookRequest) (operationresult.Result[struct{}], error)
	CurrentConfirmation(context.Context, control.CurrentConfirmationRequest) (operationresult.Result[control.CurrentConfirmationResult], error)
	RespondConfirmation(context.Context, control.RespondConfirmationRequest) (operationresult.Result[control.RespondConfirmationResult], error)
	QueryTools(context.Context, tool.ToolQuery) (operationresult.Result[[]tool.ToolInfo], error)
	LoadToolDetail(context.Context, control.ToolDetailRequest) (operationresult.Result[tool.DetailBlock], error)
	ReloadSkills(context.Context) (operationresult.Result[struct{}], error)
	RemoveSkill(context.Context, control.RemoveSkillRequest) (operationresult.Result[struct{}], error)
	ListRollbacks(context.Context) (operationresult.Result[[]fileops.RollbackInfo], error)
	RollbackByID(context.Context, control.RollbackByIDRequest) (operationresult.Result[fileops.RollbackResult], error)
	ListFiles(context.Context, control.ListFilesRequest) (operationresult.Result[control.ListFilesResult], error)
	ReadFile(context.Context, control.ReadFileRequest) (operationresult.Result[control.ReadFileResult], error)
	WriteFile(context.Context, control.WriteFileRequest) (operationresult.Result[control.WriteFileResult], error)
	Workspace(context.Context) (operationresult.Result[control.WorkspaceResult], error)
	WorkspaceFiles(context.Context, control.WorkspaceFilesRequest) (operationresult.Result[control.WorkspaceFilesResult], error)
	WorkspaceDirectories(context.Context, control.WorkspaceDirectoriesRequest) (operationresult.Result[control.WorkspaceDirectoriesResult], error)
	SetUserWorkspace(context.Context, control.SetUserWorkspaceRequest) (operationresult.Result[control.WorkspaceResult], error)
	SetWorkspaceSync(context.Context, control.SetWorkspaceSyncRequest) (operationresult.Result[control.WorkspaceResult], error)
	WorkspaceGit(context.Context, control.WorkspaceGitRequest) (operationresult.Result[control.WorkspaceGitResult], error)
	WorkspaceGitDiff(context.Context, control.WorkspaceGitRequest) (operationresult.Result[string], error)
	RequestRestart(context.Context) (operationresult.Result[control.RequestRestartResult], error)
}
