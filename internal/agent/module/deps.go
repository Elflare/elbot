package module

import (
	"context"
	"time"

	"elbot/internal/agent"
	"elbot/internal/agent/dialogue"
	commandbuiltin "elbot/internal/command/builtin"
	"elbot/internal/config"
	"elbot/internal/delivery/dispatch"
	"elbot/internal/events"
	"elbot/internal/fileops"
	"elbot/internal/hook"
	"elbot/internal/media"
	"elbot/internal/memory/resident"
	"elbot/internal/modelmgr"
	"elbot/internal/security"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/tool/skill"
	"elbot/internal/toolrun"
)

// ModuleDeps declares external capabilities, never sibling modules or their complete dependency sets.
type ModuleDeps struct {
	Reasoning    func(context.Context) dialogue.ReasoningStream // Optional direct reasoning display.
	PlatformName string
	Store        storage.Store
	Models       *modelmgr.Service
	Policy       *security.Policy
	Media        *media.Manager
	Dispatcher   *dispatch.Router
	Doctor       commandbuiltin.DoctorService
	Registry     *tool.Registry
	ToolManager  *toolrun.Manager
	Preloader    *toolrun.PreloadService
	ToolState    *toolrun.StateService
	Files        *fileops.Service
	Skills       *skill.Manager
	Memory       *resident.Store
	HookRunner   interface {
		Run(context.Context, hook.Event) (hook.Event, events.LogResult, error)
		Notify(context.Context, hook.Event) (events.LogResult, error)
	}
	HookRouter      agent.HookRouter
	HookControl     commandbuiltin.HookService
	Session         config.SessionConfig
	Context         config.ContextConfig
	Prefixes        []string
	PageSize        int           // Session command list page size.
	RetentionDays   int           // Session cleanup retention exposed by session commands.
	MaxRounds       int           // Maximum tool rounds in a turn.
	ResponseTimeout time.Duration // Model response timeout.
	SoulPath        string
	LogDir          string
}
