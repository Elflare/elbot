package agent

import (
	"context"
	"fmt"
	"strings"

	"elbot/internal/agent/dialogue"
	"elbot/internal/agent/routes"
	"elbot/internal/command"
	"elbot/internal/contextmgr"
	"elbot/internal/delivery/dispatch"
	"elbot/internal/fileops"
	"elbot/internal/hook"
	"elbot/internal/media"
	"elbot/internal/memory/resident"
	"elbot/internal/modelmgr"
	"elbot/internal/notification"
	"elbot/internal/platform"
	"elbot/internal/request"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/toolrun"
	"elbot/internal/turn"
)

// Dependencies is used only while assembling the module.
type Dependencies struct {
	Routes              *routes.Registry
	Commands            *command.Router
	Sessions            *session.Service
	Requests            *request.Manager
	Turns               *turn.Manager
	ToolRunner          *toolrun.Manager
	ToolPreloader       *toolrun.PreloadService
	Platform            platform.PlatformAdapter
	Models              *modelmgr.Service
	Contexts            *contextmgr.Service
	ToolState           *toolrun.StateService
	Store               storage.Store
	Media               *media.Manager
	ResidentMemoryStore *resident.Store
	HookManager         hook.Manager
	HookRuntime         HookRouter
	Dispatcher          *dispatch.Router
	Notifications       *notification.Manager
	Logs                LogManager
	ToolRegistry        *tool.Registry
	FileRollback        *fileops.Service
	ToolProvider        dialogue.ToolSchemaProvider
	SecurityPolicy      *security.Policy
}

func validateConstruction(ctx context.Context, cfg Config, deps Dependencies) error {
	if ctx == nil {
		return fmt.Errorf("runtime context is required")
	}
	if deps.Routes == nil {
		return fmt.Errorf("provider bindings are required")
	}
	if deps.Models == nil {
		return fmt.Errorf("model service is required")
	}
	if deps.Store == nil {
		return fmt.Errorf("store is required")
	}
	if deps.Platform == nil {
		return fmt.Errorf("platform is required")
	}
	if deps.Sessions == nil || deps.Requests == nil || deps.Turns == nil || deps.Commands == nil {
		return fmt.Errorf("session, request, turn and command services are required")
	}
	if deps.Contexts == nil || deps.ToolState == nil || deps.ToolRunner == nil || deps.ToolPreloader == nil {
		return fmt.Errorf("context and tool services are required")
	}
	if deps.Dispatcher == nil || deps.Notifications == nil {
		return fmt.Errorf("delivery and notification services are required")
	}
	if strings.TrimSpace(cfg.SandboxRoot) == "" {
		return fmt.Errorf("sandbox root is required")
	}
	if cfg.ToolsConfig.MaxRoundsPerTurn <= 0 {
		return fmt.Errorf("tools max rounds per turn must be positive")
	}
	if deps.SecurityPolicy == nil {
		return fmt.Errorf("security policy is required")
	}
	return nil
}
