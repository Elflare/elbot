package control

import (
	"context"

	"elbot/internal/operationresult"
)

func (m *Supervisor) CurrentConfirmation(ctx context.Context, req CurrentConfirmationRequest) (operationresult.Result[CurrentConfirmationResult], error) {
	authorized, err := authorize(ctx, false)
	if err != nil || authorized.Status != operationresult.Success {
		return operationresult.Result[CurrentConfirmationResult]{Status: authorized.Status, Message: authorized.Message}, err
	}
	return m.deps.Confirmations.Current(ctx, req)
}

func (m *Supervisor) RespondConfirmation(ctx context.Context, req RespondConfirmationRequest) (operationresult.Result[RespondConfirmationResult], error) {
	authorized, err := authorize(ctx, false)
	if err != nil || authorized.Status != operationresult.Success {
		return operationresult.Result[RespondConfirmationResult]{Status: authorized.Status, Message: authorized.Message}, err
	}
	if req.Decision != "approve" && req.Decision != "reject" {
		return operationresult.Result[RespondConfirmationResult]{Status: operationresult.InvalidArguments, Message: "decision 必须是 approve 或 reject"}, nil
	}
	if req.Scope != "current" && req.Scope != "tool" && req.Scope != "all" {
		return operationresult.Result[RespondConfirmationResult]{Status: operationresult.InvalidArguments, Message: "scope 必须是 current、tool 或 all"}, nil
	}
	return m.deps.Confirmations.Respond(ctx, req)
}
