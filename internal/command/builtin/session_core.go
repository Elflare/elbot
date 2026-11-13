package builtin

import (
	"context"
	command "elbot/internal/command/share"
	"elbot/internal/contextinfo"
)

func NewNew(deps Deps) command.Handler {
	return command.NewFunc(command.Info{Name: "new", Description: "重置会话", SessionEffect: command.SessionEffectSwitchCurrent, MinRole: contextinfo.RoleUser, Help: "解除当前绑定；下次对话新建，旧会话保留"}, func(ctx context.Context, req command.Request) (command.Result, error) {
		result, err := deps.Control.NewSession(ctx)
		if err != nil {
			return command.Result{}, err
		}
		return sessionChange(result), nil
	})
}
func NewStatus(deps Deps) command.Handler {
	return command.NewFunc(command.Info{Name: "status", Description: "当前会话状态", MinRole: contextinfo.RoleUser}, func(ctx context.Context, req command.Request) (command.Result, error) {
		state, err := deps.Control.Status(ctx)
		if err != nil {
			return command.Result{}, err
		}
		return command.FromOperationResult(state, command.ResultKindStatus), nil
	})
}
