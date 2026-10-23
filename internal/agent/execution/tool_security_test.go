package execution

import (
	"testing"

	"elbot/internal/contextinfo"

	"elbot/internal/security"
)

func TestAuthorizedActorsConfirmHighRiskTools(t *testing.T) {
	policy := security.DefaultPolicy()
	if !policy.NeedsToolConfirmation(contextinfo.Actor{Role: contextinfo.RoleSuperadmin}, security.RiskHigh) {
		t.Fatalf("superadmin should need confirmation for high risk")
	}
	if !policy.NeedsToolConfirmation(contextinfo.Actor{Role: contextinfo.RoleUser}, security.RiskHigh) {
		t.Fatalf("regular user should confirm high-risk authorized tools")
	}
	if policy.NeedsToolConfirmation(contextinfo.Actor{Role: contextinfo.RoleUser}, security.RiskMedium) {
		t.Fatalf("regular user should not confirm medium-risk tools")
	}
	if !policy.CanUseTool(contextinfo.Actor{Role: contextinfo.RoleUser}, security.RiskHigh, true) {
		t.Fatalf("regular user should be allowed for owner-scoped high risk")
	}
	if policy.CanUseTool(contextinfo.Actor{Role: contextinfo.RoleUser}, security.RiskHigh, false) {
		t.Fatalf("regular user should be denied for non-owner high risk")
	}
}
