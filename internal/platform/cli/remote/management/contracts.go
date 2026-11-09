package management

import (
	"context"
	"elbot/internal/control"
	"elbot/internal/fileops"
	"elbot/internal/tool"
)

// ControlCapabilities is the business surface consumed by the management API.
type ControlCapabilities interface {
	ListCommands(context.Context, control.ListCommandsRequest) (control.ListCommandsResult, error)
	Status(context.Context) (control.StatusResult, error)
	ListSessions(context.Context, control.ListSessionsRequest) (control.ListSessionsResult, error)
	GetSession(context.Context, control.GetSessionRequest) (control.GetSessionResult, error)
	NewSession(context.Context) (control.NewSessionResult, error)
	RenameSession(context.Context, control.RenameSessionRequest) (control.RenameSessionResult, error)
	SetSessionArchived(context.Context, control.SetSessionArchivedRequest) (control.SetSessionArchivedResult, error)
	SetSessionPinned(context.Context, control.SetSessionPinnedRequest) (control.SetSessionPinnedResult, error)
	DeleteSession(context.Context, control.DeleteSessionRequest) (control.DeleteSessionResult, error)
	CleanSessions(context.Context, control.CleanSessionsRequest) (control.CleanSessionsResult, error)
	ResumeSession(context.Context, control.ResumeSessionRequest) (control.ResumeSessionResult, error)
	ForkSession(context.Context, control.ForkSessionRequest) (control.ForkSessionResult, error)
	CompactSession(context.Context, control.CompactSessionRequest) (control.CompactSessionResult, error)
	SetMode(context.Context, control.SetModeRequest) (control.SetModeResult, error)
	SessionMessages(context.Context, control.SessionMessagesRequest) (control.SessionMessagesResult, error)
	ListModels(context.Context, control.ListModelsRequest) (control.ListModelsResult, error)
	RefreshModels(context.Context, control.ListModelsRequest) (control.RefreshModelsResult, error)
	SelectModel(context.Context, control.SelectModelRequest) (control.SelectModelResult, error)
	ListRequests(context.Context) (control.ListRequestsResult, error)
	StopRequest(context.Context, control.StopRequestRequest) (control.StopRequestResult, error)
	StopSession(context.Context) (control.StopSessionResult, error)
	StopAllRequests(context.Context) (control.StopAllRequestsResult, error)
	QueryLogs(context.Context, control.QueryLogsRequest) (control.QueryLogsResult, error)
	QueryUsage(context.Context, control.QueryUsageRequest) (control.QueryUsageResult, error)
	CheckConfiguration(context.Context) (control.CheckConfigurationResult, error)
	ListHooks(context.Context) (control.ListHooksResult, error)
	GetHook(context.Context, control.GetHookRequest) (control.GetHookResult, error)
	ReloadHooks(context.Context) (control.ReloadHooksResult, error)
	StartHook(context.Context, control.StartHookRequest) error
	StopHook(context.Context, control.StopHookRequest) error
	RestartHook(context.Context, control.RestartHookRequest) error
	CurrentConfirmation(context.Context, control.CurrentConfirmationRequest) (control.CurrentConfirmationResult, error)
	RespondConfirmation(context.Context, control.RespondConfirmationRequest) (control.RespondConfirmationResult, error)
	QueryTools(context.Context, tool.ToolQuery) ([]tool.ToolInfo, error)
	LoadToolDetail(context.Context, control.ToolDetailRequest) (tool.DetailBlock, error)
	ReloadSkills(context.Context) error
	RemoveSkill(context.Context, control.RemoveSkillRequest) error
	ListRollbacks(context.Context) ([]fileops.RollbackInfo, error)
	RollbackByID(context.Context, control.RollbackByIDRequest) (fileops.RollbackResult, error)
	ListFiles(context.Context, control.ListFilesRequest) (control.ListFilesResult, error)
	ReadFile(context.Context, control.ReadFileRequest) (control.ReadFileResult, error)
	WriteFile(context.Context, control.WriteFileRequest) (control.WriteFileResult, error)
	Workspace(context.Context) (control.WorkspaceResult, error)
	WorkspaceFiles(context.Context, control.WorkspaceFilesRequest) (control.WorkspaceFilesResult, error)
	WorkspaceDirectories(context.Context, control.WorkspaceDirectoriesRequest) (control.WorkspaceDirectoriesResult, error)
	SetUserWorkspace(context.Context, control.SetUserWorkspaceRequest) (control.WorkspaceResult, error)
	SetWorkspaceSync(context.Context, control.SetWorkspaceSyncRequest) (control.WorkspaceResult, error)
	WorkspaceGit(context.Context, control.WorkspaceGitRequest) (control.WorkspaceGitResult, error)
	WorkspaceGitDiff(context.Context, control.WorkspaceGitRequest) (string, error)
	RequestRestart(context.Context) (control.RequestRestartResult, error)
}
