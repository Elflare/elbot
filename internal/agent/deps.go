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
	"elbot/internal/tool/share"
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
	QueryTools(context.Context, share.ToolQuery) ([]share.Info, error)
	ToolTags(context.Context) ([]string, error)
	ToolNamesByTag(context.Context, string, share.ToolQuery) ([]string, error)
	Schemas(context.Context, share.Context, []share.DiscoveredTool) ([]llm.ToolSchema, error)
	Run(context.Context, share.RunnerDeps, share.RunRequest) share.RunResult
	DiscoveredToolsForSession(context.Context, string, bool) ([]share.DiscoveredTool, events.LogResult, error)
	DecodeState(string) (share.State, error)
	Commit(context.Context, string, share.StateUpdate) (share.StateCommit, events.LogResult, error)
	PrepareTools(context.Context, []string) (share.ToolPreload, error)
	PrepareSkills(context.Context, string, []string) (share.SkillPreload, error)
	TagPrompts(context.Context, []string) ([]share.TagPrompt, error)
	Read(context.Context, session.Scope) (resident.Memory, error)
	WithBinding(context.Context, fileops.Binding, ...fileops.CommitAdmission) context.Context
	Forget(fileops.Binding)
	ListRollbacks(context.Context) ([]fileops.RollbackInfo, error)
	RollbackByID(context.Context, uint64) (fileops.RollbackResult, error)
}

// HookCapabilities supplies only Hook management commands. Execution callbacks
// are registered separately through RegisterHooks.
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
