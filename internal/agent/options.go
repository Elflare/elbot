package agent

import (
	"fmt"
	"path/filepath"
	"strings"

	"elbot/internal/command"
	"elbot/internal/config"
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

// Options groups the agent's construction-time dependencies and configuration.
type Options struct {
	Commands              *command.Router
	Sessions              *session.Service
	Requests              *request.Manager
	Turns                 *turn.Manager
	ToolRunner            *toolrun.Manager
	ToolPreloader         *toolrun.PreloadService
	Platform              platform.PlatformAdapter
	Models                *modelmgr.Service
	Contexts              *contextmgr.Service
	ToolState             *toolrun.StateService
	Store                 storage.Store
	Media                 *media.Manager
	SoulPath              string
	ResidentMemoryStore   *resident.Store
	LLMRequestConfig      config.LLMRequestConfig
	HookManager           hook.Manager
	HookRuntime           HookRouter
	Dispatcher            *dispatch.Router
	Notifications         *notification.Manager
	Logs                  LogManager
	ToolRegistry          *tool.Registry
	FileRollback          *fileops.Service
	ToolProvider          ToolSchemaProvider
	SecurityPolicy        *security.Policy
	SessionIdleExpiration config.SessionIdleExpirationConfig
	SandboxRoot           string
	ToolsConfig           config.ToolsConfig
}

func validateOptions(opts Options) error {
	if opts.Models == nil {
		return fmt.Errorf("model service is required")
	}
	if opts.Store == nil {
		return fmt.Errorf("store is required")
	}
	if opts.Platform == nil {
		return fmt.Errorf("platform is required")
	}
	if opts.Sessions == nil || opts.Requests == nil || opts.Turns == nil || opts.Commands == nil {
		return fmt.Errorf("session, request, turn and command services are required")
	}
	if opts.Contexts == nil || opts.ToolState == nil || opts.ToolRunner == nil || opts.ToolPreloader == nil {
		return fmt.Errorf("context and tool services are required")
	}
	if opts.Dispatcher == nil || opts.Notifications == nil {
		return fmt.Errorf("delivery and notification services are required")
	}
	if strings.TrimSpace(opts.SandboxRoot) == "" {
		return fmt.Errorf("sandbox root is required")
	}
	if opts.ToolsConfig.MaxRoundsPerTurn <= 0 {
		return fmt.Errorf("tools max rounds per turn must be positive")
	}
	if opts.SecurityPolicy == nil {
		return fmt.Errorf("security policy is required")
	}
	return nil
}

func (a *Agent) SetSessionIdleExpiration(cfg config.SessionIdleExpirationConfig) {
	a.waitPolicy.idleExpiration = sessionIdleExpirationConfig(cfg)
}

func sessionIdleExpirationConfig(cfg config.SessionIdleExpirationConfig) session.IdleExpirationConfig {
	return session.IdleExpirationConfig{
		GroupUserTTLMinutes:         cfg.GroupUserTTLMinutes,
		GroupSuperadminTTLMinutes:   cfg.GroupSuperadminTTLMinutes,
		PrivateUserTTLMinutes:       cfg.PrivateUserTTLMinutes,
		PrivateSuperadminTTLMinutes: cfg.PrivateSuperadminTTLMinutes,
	}
}

func (a *Agent) SetSandboxRoot(root string) {
	root = filepath.Clean(root)
	if root == "." || root == "" {
		root = config.Default().Sandbox.Root
	}
	a.sandboxRoot = root
}

func (a *Agent) SetSecurityPolicy(policy *security.Policy) {
	if policy == nil {
		policy = security.DefaultPolicy()
	}
	a.identity.policy = policy
}
