package control

import (
	"context"

	"elbot/internal/control/share"
	"elbot/internal/operationresult"
	"elbot/internal/session"
	"elbot/internal/storage"
)

func (m *Supervisor) Coagent(ctx context.Context, action session.CoagentAction, user bool) (operationresult.Result[storage.CoagentBoard], error) {
	auth, err := share.Authorize(ctx, false)
	if err != nil || auth.Status != operationresult.Success {
		return operationresult.Result[storage.CoagentBoard]{Status: auth.Status, Message: auth.Message}, err
	}
	return m.deps.Coagents(ctx, action, user)
}
