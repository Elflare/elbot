package control

import (
	"context"

	"elbot/internal/control/share"
	"elbot/internal/operationresult"
)

func (m *Supervisor) CurrentConfirmation(ctx context.Context, req CurrentConfirmationRequest) (operationresult.Result[CurrentConfirmationResult], error) {
	authorized, err := share.Authorize(ctx, false)
	if err != nil {
		return operationresult.Result[CurrentConfirmationResult]{}, err
	}
	if authorized.Status != operationresult.Success {
		return operationresult.Result[CurrentConfirmationResult]{Status: authorized.Status, Message: authorized.Message}, nil
	}
	return m.deps.Confirmations.Current(ctx, req)
}

func (m *Supervisor) RespondConfirmation(ctx context.Context, req RespondConfirmationRequest) (operationresult.Result[RespondConfirmationResult], error) {
	authorized, err := share.Authorize(ctx, false)
	if err != nil {
		return operationresult.Result[RespondConfirmationResult]{}, err
	}
	if authorized.Status != operationresult.Success {
		return operationresult.Result[RespondConfirmationResult]{Status: authorized.Status, Message: authorized.Message}, nil
	}
	if req.Decision != "approve" && req.Decision != "reject" {
		return operationresult.Result[RespondConfirmationResult]{Status: operationresult.InvalidArguments, Message: "decision 必须是 approve 或 reject"}, nil
	}
	if req.Scope != "current" && req.Scope != "tool" && req.Scope != "all" {
		return operationresult.Result[RespondConfirmationResult]{Status: operationresult.InvalidArguments, Message: "scope 必须是 current、tool 或 all"}, nil
	}
	return m.deps.Confirmations.Respond(ctx, req)
}
