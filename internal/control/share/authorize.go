package share

import (
	"context"

	"elbot/internal/contextinfo"
	"elbot/internal/operationresult"
)

// Authorize allows user and superadmin actors, or only superadmin when adminOnly is true.
func Authorize(ctx context.Context, adminOnly bool) (operationresult.Result[contextinfo.Actor], error) {
	if err := ctx.Err(); err != nil {
		return operationresult.Result[contextinfo.Actor]{}, err
	}
	actor, ok := contextinfo.ActorFromContext(ctx)
	if !ok || (actor.Role != contextinfo.RoleUser && actor.Role != contextinfo.RoleSuperadmin) || (adminOnly && actor.Role != contextinfo.RoleSuperadmin) {
		return operationresult.Result[contextinfo.Actor]{Status: operationresult.Forbidden, Message: "无权执行该操作"}, nil
	}
	return operationresult.Result[contextinfo.Actor]{Status: operationresult.Success, Data: actor}, nil
}
