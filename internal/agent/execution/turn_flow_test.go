package execution

import (
	"context"

	"testing"

	"time"

	agentconfirmation "elbot/internal/agent/confirmation"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/config"
	"elbot/internal/contextinfo"

	"elbot/internal/platform"
)

func TestConfirmationWaitTimeoutUsesSessionTTLAsUpperBound(t *testing.T) {
	tests := []struct {
		name    string
		role    contextinfo.Role
		scopeID string
		cfg     config.SessionIdleExpirationConfig
		want    time.Duration
	}{
		{
			name:    "user without session TTL",
			role:    contextinfo.RoleUser,
			scopeID: "private:1",
			cfg:     config.SessionIdleExpirationConfig{},
			want:    10 * time.Minute,
		},
		{
			name:    "user shorter session TTL",
			role:    contextinfo.RoleUser,
			scopeID: "group:1",
			cfg:     config.SessionIdleExpirationConfig{GroupUserTTLMinutes: 5},
			want:    5 * time.Minute,
		},
		{
			name:    "user longer session TTL",
			role:    contextinfo.RoleUser,
			scopeID: "group:1",
			cfg:     config.SessionIdleExpirationConfig{GroupUserTTLMinutes: 20},
			want:    10 * time.Minute,
		},
		{name: "superadmin without session TTL", role: contextinfo.RoleSuperadmin, scopeID: "private:1", cfg: config.SessionIdleExpirationConfig{}, want: 0},
		{
			name:    "superadmin session TTL",
			role:    contextinfo.RoleSuperadmin,
			scopeID: "group:1",
			cfg:     config.SessionIdleExpirationConfig{GroupSuperadminTTLMinutes: 20},
			want:    20 * time.Minute,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

			policy := agentconfirmation.NewConfirmationPolicy(agentconfirmation.ConfirmationPolicyDeps{
				Identity:                agentshare.NewIdentityResolver(agentshare.IdentityResolverDeps{PlatformName: "test"}),
				IdleExpiration:          sessionIdleExpirationConfig(tt.cfg),
				UserConfirmationTimeout: 10 * time.Minute,
			})
			ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{
				Conversation: contextinfo.Conversation{Source: contextinfo.Source{Platform: "test", ScopeID: tt.scopeID}, Identity: contextinfo.Identity{PlatformUserID: "1"}},
			})
			ctx = contextinfo.WithActor(ctx, contextinfo.Actor{ID: "test:1", Role: tt.role})
			if got := policy.WaitTimeout(ctx); got != tt.want {
				t.Fatalf("confirmation timeout = %s, want %s", got, tt.want)
			}
		})
	}
}
