package tool

import (
	"context"

	"elbot/internal/operationresult"
	"elbot/internal/session"
	"elbot/internal/storage"
)

type CoagentOperations interface {
	Coagent(context.Context, session.CoagentAction, bool) (operationresult.Result[storage.CoagentBoard], error)
}
