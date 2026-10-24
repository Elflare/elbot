package agent

import (
	"context"

	agenthooks "elbot/internal/agent/hooks"
	commandbuiltin "elbot/internal/command/builtin"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/fileops"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/logging"
	"elbot/internal/media"
	"elbot/internal/memory/resident"
	"elbot/internal/modelmgr"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/toolrun"
)

// SupervisorDeps borrows shared resources and business capabilities; their owners retain lifecycle control.
type SupervisorDeps struct {
	Store      storage.Store
	Models     *modelmgr.Service
	Media      *media.Manager
	Permission *security.Permission // Shared permission permission; Agent does not own its lifecycle.
	Logs       *logging.Manager
	Tools      ToolCapabilities
	Hooks      HookCapabilities
	Platform   PlatformCapabilities
}

// ToolCapabilities declares the Tool operations consumed by Agent without exposing owned components.
type ToolCapabilities interface {
	commandbuiltin.ToolService
	commandbuiltin.SkillService
	Get(string) (tool.Tool, bool)
	ToolTags(context.Context) ([]string, error)
	ToolNamesByTag(context.Context, string, func(tool.Tool) bool) ([]string, error)
	ToolInfos(context.Context, toolrun.Context) ([]tool.Info, error)
	Schemas(context.Context, toolrun.Context, []toolrun.CachedTool) ([]llm.ToolSchema, error)
	Run(context.Context, toolrun.RunnerDeps, toolrun.RunRequest) toolrun.RunResult
	Load(context.Context, string) (*storage.Session, error)
	Commit(context.Context, string, toolrun.StateUpdate) (toolrun.StateCommit, events.LogResult, error)
	PrepareTools(context.Context, []string) (toolrun.ToolPreload, error)
	PrepareSkills(context.Context, string, []string) (toolrun.SkillPreload, error)
	TagPrompts(context.Context, []string) ([]toolrun.TagPrompt, error)
	Read(context.Context, session.Scope) (resident.Memory, error)
	WithBinding(context.Context, fileops.Binding, ...fileops.CommitAdmission) context.Context
	Forget(fileops.Binding)
	ListRollbacks(context.Context) ([]fileops.RollbackInfo, error)
	RollbackByID(context.Context, uint64) (fileops.RollbackResult, error)
}

// HookCapabilities exposes event execution, routing and Hook commands, excluding owner lifecycle methods.
type HookCapabilities interface {
	agenthooks.HookRouter
	commandbuiltin.HookService
	Run(context.Context, hook.Event) (hook.Event, events.LogResult, error)
	Notify(context.Context, hook.Event) (events.LogResult, error)
}

// PlatformCapabilities supplies platform identity and delivery for Agent output.
type PlatformCapabilities interface {
	PrimaryName() string
	SendTo(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, events.LogResult, error)
	SendChat(context.Context, []delivery.Output) (delivery.Receipt, events.LogResult, error)
	StartStream(context.Context) (delivery.MessageStream, error)
	StartReasoning(context.Context) delivery.ReasoningStream
}
