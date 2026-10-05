package agent

import (
	"fmt"
	"path/filepath"
	"strings"

	agentcommands "elbot/internal/agent/commands"
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
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/toolrun"
)

// Options groups the agent's construction-time dependencies and configuration.
type Options struct {
	Doctor                agentcommands.DoctorService
	Platform              platform.PlatformAdapter
	Models                *modelmgr.Service
	Contexts              *contextmgr.Service
	ToolState             *toolrun.StateService
	Providers             map[string]config.ProviderConfig
	Store                 storage.Store
	Media                 *media.Manager
	CommandPrefixes       []string
	SessionConfig         session.Config
	NamingNotifier        session.NamingNotifier
	SoulPath              string
	ResidentMemoryStore   *resident.Store
	LLMRequestConfig      config.LLMRequestConfig
	HookService           agentcommands.HookService
	HookManager           hook.Manager
	HookRuntime           HookRouter
	Dispatcher            *dispatch.Router
	Notifications         *notification.Manager
	Logs                  LogManager
	ToolRegistry          *tool.Registry
	FileRollback          *fileops.Service
	Skills                SkillLifecycle
	ToolProvider          ToolSchemaProvider
	SecurityPolicy        *security.Policy
	ContextConfig         config.ContextConfig
	ModelMetadata         config.ModelMetadataConfig
	SessionListPageSize   int
	CleanupRetentionDays  int
	MediaRetentionDays    int
	SessionIdleExpiration config.SessionIdleExpirationConfig
	SandboxRoot           string
	ToolsConfig           config.ToolsConfig
	ToolTagsPath          string
	ToolTags              config.ToolTagsConfig
}

func validateOptions(opts Options) error {
	if opts.Models == nil {
		return fmt.Errorf("model service is required")
	}
	if opts.Store == nil {
		return fmt.Errorf("store is required")
	}
	if opts.SessionConfig.DefaultMode == "" {
		return fmt.Errorf("session default mode is required")
	}
	if opts.SessionListPageSize <= 0 {
		return fmt.Errorf("session list page size must be positive")
	}
	if opts.CleanupRetentionDays <= 0 {
		return fmt.Errorf("cleanup retention days must be positive")
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

func (a *Agent) SetSessionListPageSize(size int) {
	if size <= 0 {
		size = config.Default().View.SessionListPageSize
	}
	a.sessionCommands.SetListPageSize(size)
}

func (a *Agent) SetCleanupRetentionDays(days int) {
	if days <= 0 {
		days = 30
	}
	a.sessionCommands.SetRetentionDays(days)
}

func (a *Agent) SetSessionIdleExpiration(cfg config.SessionIdleExpirationConfig) {
	a.idleExpiration = sessionIdleExpirationConfig(cfg)
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
	a.securityPolicy = policy
}
