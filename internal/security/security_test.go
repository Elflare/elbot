package security

import (
	"testing"

	"elbot/internal/contextinfo"
)

func TestPolicyActorKeepsDistinctNativeIdentities(t *testing.T) {
	permission := DefaultPermission()
	for _, tt := range []struct {
		userID string
		role   contextinfo.Role
	}{
		{"local", contextinfo.RoleSuperadmin},
		{"cli:local", contextinfo.RoleUser},
		{" local ", contextinfo.RoleUser},
	} {
		actor := permission.Actor("cli", tt.userID, "User")
		if actor.ActorIdentity != (contextinfo.ActorIdentity{Platform: "cli", ID: tt.userID}) || actor.ID != tt.userID || actor.Role != tt.role {
			t.Fatalf("native ID %q resolved to %+v", tt.userID, actor)
		}
	}
}

func TestPolicyUsesSeparateConfirmationThresholdsByRole(t *testing.T) {
	permission := NewPermission("low", "critical", nil)
	tests := []struct {
		name  string
		actor contextinfo.Actor
		risk  RiskLevel
		want  bool
	}{
		{name: "regular medium", actor: contextinfo.Actor{Role: contextinfo.RoleUser}, risk: RiskMedium},
		{name: "regular high", actor: contextinfo.Actor{Role: contextinfo.RoleUser}, risk: RiskHigh, want: true},
		{name: "regular critical", actor: contextinfo.Actor{Role: contextinfo.RoleUser}, risk: RiskCritical, want: true},
		{name: "superadmin high", actor: contextinfo.Actor{Role: contextinfo.RoleSuperadmin}, risk: RiskHigh},
		{name: "superadmin critical", actor: contextinfo.Actor{Role: contextinfo.RoleSuperadmin}, risk: RiskCritical, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := permission.NeedsToolConfirmation(tt.actor, tt.risk); got != tt.want {
				t.Fatalf("NeedsToolConfirmation() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCheckToolKeepsPermissionBeforeBackgroundConfirmation(t *testing.T) {
	permission := NewPermission("low", "high", nil)
	tests := []struct {
		name             string
		role             contextinfo.Role
		tool             string
		risk             RiskLevel
		ownerScoped      bool
		superadminOnly   bool
		autoConfirmShell bool
		want             ToolPermissionResult
	}{
		{name: "foreground high needs confirmation", role: contextinfo.RoleSuperadmin, tool: "shell", risk: RiskHigh, want: ToolPermissionResult{Allowed: true, NeedsConfirmation: true}},
		{name: "cron high skips confirmation", role: contextinfo.RoleSuperadmin, tool: "shell", risk: RiskHigh, autoConfirmShell: true, want: ToolPermissionResult{Allowed: true, Reason: "background_shell_auto_confirmed"}},
		{name: "elnis high skips confirmation", role: contextinfo.RoleSuperadmin, tool: "shell", risk: RiskHigh, autoConfirmShell: true, want: ToolPermissionResult{Allowed: true, Reason: "background_shell_auto_confirmed"}},
		{name: "critical cron shell rejected", role: contextinfo.RoleSuperadmin, tool: "shell", risk: RiskCritical, autoConfirmShell: true, want: ToolPermissionResult{Reason: "background_shell_rejected"}},
		{name: "automatic confirmation grants no permission", role: contextinfo.RoleUser, tool: "shell", risk: RiskHigh, autoConfirmShell: true, want: ToolPermissionResult{Reason: "tool_risk_above_allowed_level"}},
		{name: "owner scope does not bypass superadmin requirement", role: contextinfo.RoleUser, tool: "shell", risk: RiskLow, ownerScoped: true, superadminOnly: true, autoConfirmShell: true, want: ToolPermissionResult{Reason: "tool_requires_superadmin"}},
		{name: "another tool keeps normal confirmation", role: contextinfo.RoleSuperadmin, tool: "send_file", risk: RiskHigh, autoConfirmShell: true, want: ToolPermissionResult{Allowed: true, NeedsConfirmation: true}},
		{name: "another background kind keeps normal confirmation", role: contextinfo.RoleSuperadmin, tool: "shell", risk: RiskHigh, autoConfirmShell: false, want: ToolPermissionResult{Allowed: true, NeedsConfirmation: true}},
		{name: "ordinary low risk allowed", role: contextinfo.RoleUser, tool: "read_file", risk: RiskLow, want: ToolPermissionResult{Allowed: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := permission.CheckTool(contextinfo.Actor{Role: tt.role}, tt.tool, tt.risk, tt.ownerScoped, tt.superadminOnly, tt.autoConfirmShell)
			if got != tt.want {
				t.Fatalf("CheckTool() = %#v, want %#v", got, tt.want)
			}
		})
	}
}
