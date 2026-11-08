package agent

import (
	"context"

	"elbot/internal/command"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/control"
	"elbot/internal/delivery"
	"elbot/internal/fileops"
	"elbot/internal/llm"
	"elbot/internal/logging"
	"elbot/internal/media"
	"elbot/internal/memory/resident"
	"elbot/internal/modelmgr"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
)

// SupervisorDeps borrows shared resources and business capabilities; their owners retain lifecycle control.
type SupervisorDeps struct {
	Control     *control.Supervisor // App 创建的统一操作入口，Agent 仅接入自身能力并供命令消费。
	Store       storage.Store
	ChatHistory interface {
		GetByPlatformMessage(context.Context, string, contextinfo.ChatType, string, string) (*storage.ChatMessage, error)
	} // 借用历史身份查询，用于入站媒体关联。
	Models     ModelCapabilities
	Media      *media.MediaCenter
	Permission *security.Permission // Shared permission service; Agent does not own its lifecycle.
	Logs       *logging.Manager
	Tools      ToolCapabilities
	Platform   PlatformCapabilities
}

// ModelCapabilities 声明 Agent 的模型选择和管理命令能力；生命周期归 App。
type ModelCapabilities interface {
	Resolve(config.ModelSelection) modelmgr.Selection
	ResolveMode(string) modelmgr.Selection
	ResolveCompact(modelmgr.Selection) modelmgr.Selection
	ResolveNaming(context.Context) modelmgr.NamingSelection
	ReloadProviders(context.Context) error
	CurrentModelForMode(mode string) modelmgr.ModelOption
	CurrentCompactModel(mode string) modelmgr.ModelOption
	PrepareModel(arg string) (modelmgr.ModelOption, error)
	CommitModelForMode(mode string, selected modelmgr.ModelOption) (modelmgr.ModelOption, error)
	SelectCompactModel(arg string) (modelmgr.ModelOption, error)
	SelectNamingModel(arg string) (modelmgr.ModelOption, error)
	ModelList(query string, opts modelmgr.ModelListOptions) modelmgr.ModelListResult
}

// ToolCapabilities declares the Tool operations consumed by Agent without exposing owned components.
type ToolCapabilities interface {
	Remove(context.Context, string) error
	Reload(context.Context) error
	FilterSchemas(context.Context, []llm.ToolSchema) ([]llm.ToolSchema, error)
	QueryTools(context.Context, tool.ToolQuery) ([]tool.ToolInfo, error)
	ToolTags(context.Context) ([]string, error)
	Schemas(context.Context, tool.Context, []tool.DiscoveredTool) ([]llm.ToolSchema, error)
	Run(context.Context, tool.RunnerDeps, tool.RunRequest) tool.RunResult
	DiscoveredToolsForSession(context.Context, string, bool) ([]tool.DiscoveredTool, error)
	DecodeState(string) (tool.State, error)
	Commit(context.Context, string, tool.StateUpdate) (tool.StateCommit, error)
	PrepareBackground(context.Context, tool.BackgroundPreloadRequest) (tool.BackgroundPreload, error)
	PrepareTools(context.Context, []string) (tool.ToolPreload, error)
	PrepareSkills(context.Context, string, []string) (tool.SkillPreload, error)
	TagPrompts(context.Context, []string) ([]tool.TagPrompt, error)
	Read(context.Context, session.Scope) (resident.Memory, error)
	WithBinding(context.Context, fileops.Binding, ...fileops.CommitAdmission) context.Context
	Forget(fileops.Binding)
	ListRollbacks(context.Context) ([]fileops.RollbackInfo, error)
	RollbackByID(context.Context, uint64) (fileops.RollbackResult, error)
}

// PlatformCapabilities supplies platform identity and delivery for Agent output.
type PlatformCapabilities interface {
	SendCommandResult(context.Context, command.Request, *command.Result, *command.Error) (delivery.Receipt, error)
	SessionIDCode(string, contextinfo.ChatType) (string, error)
	RestoreMessageContext(context.Context, context.Context) context.Context
	SendTo(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, error)
	StartStream(context.Context) (delivery.MessageStream, error)
}
