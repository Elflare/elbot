package control

import (
	"context"
)

func (m *Supervisor) CurrentConfirmation(ctx context.Context, req CurrentConfirmationRequest) (CurrentConfirmationResult, error) {
	if err := authorize(ctx, false, m.deps.Confirmations != nil); err != nil {
		return CurrentConfirmationResult{}, err
	}
	result, err := m.deps.Confirmations.Current(ctx, req)
	return result, ClassifyError(err)
}

func (m *Supervisor) RespondConfirmation(ctx context.Context, req RespondConfirmationRequest) (RespondConfirmationResult, error) {
	if err := authorize(ctx, false, m.deps.Confirmations != nil); err != nil {
		return RespondConfirmationResult{}, err
	}
	if req.Decision != "approve" && req.Decision != "reject" {
		return RespondConfirmationResult{}, invalid("decision 必须是 approve 或 reject")
	}
	if req.Scope != "current" && req.Scope != "tool" && req.Scope != "all" {
		return RespondConfirmationResult{}, invalid("scope 必须是 current、tool 或 all")
	}
	result, err := m.deps.Confirmations.Respond(ctx, req)
	return result, ClassifyError(err)
}
