package agent

import (
	"context"

	commandbuiltin "elbot/internal/command/builtin"
	"elbot/internal/delivery"
	"elbot/internal/events"
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
	Store      storage.Store
	Models     *modelmgr.Service
	Media      *media.Manager
	Permission *security.Permission // Shared permission service; Agent does not own its lifecycle.
	Logs       *logging.Manager
	Tools      ToolCapabilities
	Hooks      HookCapabilities
	Platform   PlatformCapabilities
}

// ToolCapabilities declares the Tool operations consumed by Agent without exposing owned components.
type ToolCapabilities interface {
	List() []tool.Info
	Unregister(string) error
	Remove(context.Context, string) error
	Reload(context.Context) error
	ToolInfo(string) (tool.Info, bool)
	FilterSchemas(context.Context, []llm.ToolSchema) ([]llm.ToolSchema, error)
	QueryTools(context.Context, tool.ToolQuery) ([]tool.Info, error)
	ToolTags(context.Context) ([]string, error)
	ToolNamesByTag(context.Context, string, tool.ToolQuery) ([]string, error)
	Schemas(context.Context, tool.Context, []tool.DiscoveredTool) ([]llm.ToolSchema, error)
	Run(context.Context, tool.RunnerDeps, tool.RunRequest) tool.RunResult
	DiscoveredToolsForSession(context.Context, string, bool) ([]tool.DiscoveredTool, events.LogResult, error)
	DecodeState(string) (tool.State, error)
	Commit(context.Context, string, tool.StateUpdate) (tool.StateCommit, events.LogResult, error)
	PrepareTools(context.Context, []string) (tool.ToolPreload, error)
	PrepareSkills(context.Context, string, []string) (tool.SkillPreload, error)
	TagPrompts(context.Context, []string) ([]tool.TagPrompt, error)
	Read(context.Context, session.Scope) (resident.Memory, error)
	WithBinding(context.Context, fileops.Binding, ...fileops.CommitAdmission) context.Context
	Forget(fileops.Binding)
	ListRollbacks(context.Context) ([]fileops.RollbackInfo, error)
	RollbackByID(context.Context, uint64) (fileops.RollbackResult, error)
}

// HookCapabilities supplies only Hook management commands. Execution callbacks
// are registered separately through RegisterHooks.
// TODO: Hook 管理结果迁到其根包后，在此声明根包类型的消费契约；当前沿用提供方已有签名。
type HookCapabilities interface {
	commandbuiltin.HookService
}

// PlatformCapabilities supplies platform identity and delivery for Agent output.
type PlatformCapabilities interface {
	PrimaryName() string
	SendTo(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, events.LogResult, error)
	SendChat(context.Context, []delivery.Output) (delivery.Receipt, events.LogResult, error)
	StartStream(context.Context) (delivery.MessageStream, error)
	StartReasoning(context.Context) delivery.ReasoningStream
}
