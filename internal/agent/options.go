package agent

import (
	"fmt"
	"path/filepath"
	"strings"

	agentcommands "elbot/internal/agent/commands"
	"elbot/internal/config"
	"elbot/internal/delivery"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/memory/resident"
	"elbot/internal/platform"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
)

// Options groups the agent's construction-time dependencies and configuration.
type Options struct {
	Platform              platform.PlatformAdapter
	Clients               map[string]llm.LLM
	ModeModels            map[string]config.ModelSelection
	Providers             map[string]config.ProviderConfig
	StatePath             string
	Store                 storage.Store
	CommandPrefixes       []string
	SessionConfig         session.Config
	NamingSelection       config.ModelSelection
	NamingNotifier        session.NamingNotifier
	SoulPath              string
	ResidentMemoryStore   *resident.Store
	LLMRequestConfig      config.LLMRequestConfig
	HookService           agentcommands.HookService
	HookManager           hook.Manager
	HookRuntime           HookRouter
	OutputManager         delivery.Manager
	Logs                  LogManager
	ToolRegistry          *tool.Registry
	Skills                SkillLifecycle
	ToolProvider          ToolSchemaProvider
	SecurityPolicy        *security.Policy
	ContextConfig         config.ContextConfig
	ModelMetadata         config.ModelMetadataConfig
	CompactModel          config.ModelSelection
	SessionListPageSize   int
	CleanupRetentionDays  int
	SessionIdleExpiration config.SessionIdleExpirationConfig
	SandboxRoot           string
	ToolsConfig           config.ToolsConfig
	ToolTagsPath          string
	ToolTags              config.ToolTagsConfig
}

func validateOptions(opts Options) error {
	workModel := opts.ModeModels[storage.SessionModeWork]
	if workModel.Provider == "" || workModel.Model == "" {
		return fmt.Errorf("mode_models.work provider/model is required")
	}
	if opts.Store == nil {
		return fmt.Errorf("store is required")
	}
	if len(opts.Providers) == 0 {
		return fmt.Errorf("at least one provider is required")
	}
	for name := range opts.Providers {
		if opts.Clients[name] == nil {
			return fmt.Errorf("client not found for provider %q", name)
		}
	}
	for mode, selection := range opts.ModeModels {
		if err := validateModelSelection("mode_models."+mode, selection, opts.Providers); err != nil {
			return err
		}
	}
	if err := validateOptionalModelSelection("naming_model", opts.NamingSelection, opts.Providers); err != nil {
		return err
	}
	if err := validateOptionalModelSelection("compact_model", opts.CompactModel, opts.Providers); err != nil {
		return err
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

func validateOptionalModelSelection(name string, selection config.ModelSelection, providers map[string]config.ProviderConfig) error {
	if selection.Provider == "" && selection.Model == "" {
		return nil
	}
	return validateModelSelection(name, selection, providers)
}

func validateModelSelection(name string, selection config.ModelSelection, providers map[string]config.ProviderConfig) error {
	if selection.Provider == "" || selection.Model == "" {
		return fmt.Errorf("%s provider/model must both be set", name)
	}
	if _, ok := providers[selection.Provider]; !ok {
		return fmt.Errorf("%s provider %q not found", name, selection.Provider)
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
