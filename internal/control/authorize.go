package control

import (
	"context"

	"elbot/internal/contextinfo"
	"elbot/internal/operationresult"
)

// authorize 接受用户和超管；adminOnly 操作只允许超管。
func authorize(ctx context.Context, adminOnly bool) (operationresult.Result[contextinfo.Actor], error) {
	if err := ctx.Err(); err != nil {
		return operationresult.Result[contextinfo.Actor]{}, err
	}
	actor, ok := contextinfo.ActorFromContext(ctx)
	if !ok || (actor.Role != contextinfo.RoleUser && actor.Role != contextinfo.RoleSuperadmin) || (adminOnly && actor.Role != contextinfo.RoleSuperadmin) {
		return operationresult.Result[contextinfo.Actor]{Status: operationresult.Forbidden, Message: "无权执行该操作"}, nil
	}
	return operationresult.Result[contextinfo.Actor]{Status: operationresult.Success, Data: actor}, nil
}
