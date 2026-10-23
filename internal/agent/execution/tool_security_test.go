package execution

import (
	"testing"

	"elbot/internal/contextinfo"

	"elbot/internal/security"
)

func TestAuthorizedActorsConfirmHighRiskTools(t *testing.T) {
	permission := security.DefaultPermission()
	if !permission.NeedsToolConfirmation(contextinfo.Actor{Role: contextinfo.RoleSuperadmin}, security.RiskHigh) {
		t.Fatalf("superadmin should need confirmation for high risk")
	}
	if !permission.NeedsToolConfirmation(contextinfo.Actor{Role: contextinfo.RoleUser}, security.RiskHigh) {
		t.Fatalf("regular user should confirm high-risk authorized tools")
	}
	if permission.NeedsToolConfirmation(contextinfo.Actor{Role: contextinfo.RoleUser}, security.RiskMedium) {
		t.Fatalf("regular user should not confirm medium-risk tools")
	}
	if !permission.CanUseTool(contextinfo.Actor{Role: contextinfo.RoleUser}, security.RiskHigh, true) {
		t.Fatalf("regular user should be allowed for owner-scoped high risk")
	}
	if permission.CanUseTool(contextinfo.Actor{Role: contextinfo.RoleUser}, security.RiskHigh, false) {
		t.Fatalf("regular user should be denied for non-owner high risk")
	}
}
