package agent

import (
	"time"

	"elbot/internal/config"
	"elbot/internal/session"
)

func responseTimeout(cfg config.LLMRequestConfig) time.Duration {
	if cfg.ResponseTimeoutSeconds <= 0 {
		return 0
	}
	return time.Duration(cfg.ResponseTimeoutSeconds) * time.Second
}

func sessionIdleExpirationConfig(cfg config.SessionIdleExpirationConfig) session.IdleExpirationConfig {
	return session.IdleExpirationConfig{
		GroupUserTTLMinutes:         cfg.GroupUserTTLMinutes,
		GroupSuperadminTTLMinutes:   cfg.GroupSuperadminTTLMinutes,
		PrivateUserTTLMinutes:       cfg.PrivateUserTTLMinutes,
		PrivateSuperadminTTLMinutes: cfg.PrivateSuperadminTTLMinutes,
	}
}
