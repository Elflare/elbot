package security

import "testing"

func TestActorIDCanonicalizesPlatformPrefix(t *testing.T) {
	if got := ActorID("qqofficial", "qqofficial:user-1"); got != "qqofficial:user-1" {
		t.Fatalf("ActorID() = %q, want qqofficial:user-1", got)
	}
}

func TestPolicyActorUsesCanonicalIDAndNativePlatformUserID(t *testing.T) {
	policy := NewPolicy("low", "high", map[string][]string{"qqofficial": {"user-1"}})
	actor := policy.Actor("user-1", "qqofficial", "qqofficial:user-1", "User")
	if actor.ID != "qqofficial:user-1" {
		t.Fatalf("actor.ID = %q, want qqofficial:user-1", actor.ID)
	}
	if actor.PlatformUserID != "user-1" {
		t.Fatalf("actor.PlatformUserID = %q, want user-1", actor.PlatformUserID)
	}
	if actor.Role != RoleSuperadmin {
		t.Fatalf("actor.Role = %q, want %q", actor.Role, RoleSuperadmin)
	}
}

func TestPolicyActorFallsBackToIDWhenPlatformUserIDMissing(t *testing.T) {
	actor := DefaultPolicy().Actor("qqofficial:user-1", "qqofficial", "", "User")
	if actor.ID != "qqofficial:user-1" {
		t.Fatalf("actor.ID = %q, want qqofficial:user-1", actor.ID)
	}
	if actor.PlatformUserID != "user-1" {
		t.Fatalf("actor.PlatformUserID = %q, want user-1", actor.PlatformUserID)
	}
}

func TestPolicyUsesSeparateConfirmationThresholdsByRole(t *testing.T) {
	policy := NewPolicy("low", "critical", nil)
	tests := []struct {
		name  string
		actor Actor
		risk  RiskLevel
		want  bool
	}{
		{name: "regular medium", actor: Actor{Role: RoleUser}, risk: RiskMedium},
		{name: "regular high", actor: Actor{Role: RoleUser}, risk: RiskHigh, want: true},
		{name: "regular critical", actor: Actor{Role: RoleUser}, risk: RiskCritical, want: true},
		{name: "superadmin high", actor: Actor{Role: RoleSuperadmin}, risk: RiskHigh},
		{name: "superadmin critical", actor: Actor{Role: RoleSuperadmin}, risk: RiskCritical, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := policy.NeedsToolConfirmation(tt.actor, tt.risk); got != tt.want {
				t.Fatalf("NeedsToolConfirmation() = %v, want %v", got, tt.want)
			}
		})
	}
}
