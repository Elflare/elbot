package agent

import (
	"path/filepath"

	agentcommands "elbot/internal/agent/commands"
	"elbot/internal/config"
	"elbot/internal/llm"
	"elbot/internal/platform"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
)

// Options groups the agent's construction-time dependencies and configuration.
type Options struct {
	Platform         platform.PlatformAdapter
	Client           llm.LLM
	ModeModels       map[string]config.ModelSelection
	Providers        map[string]config.ProviderConfig
	StatePath        string
	Store            storage.Store
	CommandPrefixes  []string
	SessionConfig    session.Config
	NamingSelection  config.ModelSelection
	NamingClient     llm.LLM
	NamingModel      string
	NamingNotifier   session.NamingNotifier
	SoulPath         string
	LLMRequestConfig config.LLMRequestConfig
	HookService      agentcommands.HookService
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
