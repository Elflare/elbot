package builtin

import (
	"context"

	command "elbot/internal/command/share"
	"elbot/internal/contextinfo"
	"elbot/internal/control"
)

func NewCompact(deps Deps) command.Handler {
	return command.NewFunc(command.Info{
		Name:          "compact",
		Args:          "",
		Description:   "压缩当前会话上下文",
		SessionEffect: command.SessionEffectMutate,
		MinRole:       contextinfo.RoleUser,
	}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		data, err := deps.Control.CompactSession(ctx, control.CompactSessionRequest{})
		return &command.Result{Kind: command.ResultKindCompact, Data: data}, err
	})
}

type CompactModule struct{}

func (CompactModule) RegisterCommands(registrar Registrar, deps Deps) error {
	return RegisterFactories(registrar, deps, NewCompact)
}
