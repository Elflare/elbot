package builtin

import (
	"context"

	command "elbot/internal/command/share"
	"elbot/internal/contextinfo"
)

func NewCompact(deps Deps) command.Handler {
	return command.NewFunc(command.Info{
		Name:          "compact",
		Args:          "",
		Description:   "压缩当前会话上下文",
		SessionEffect: command.SessionEffectMutate,
		MinRole:       contextinfo.RoleUser,
	}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		content, err := deps.Compact.CompactCurrent(ctx, "manual")
		if err != nil {
			return nil, err
		}
		return &command.Result{Kind: command.ResultKindText, Text: content}, nil
	})
}

type CompactModule struct{}

func (CompactModule) RegisterCommands(registrar Registrar, deps Deps) error {
	return RegisterFactories(registrar, deps, NewCompact)
}
