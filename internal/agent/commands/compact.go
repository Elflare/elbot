package commands

import (
	"context"

	"elbot/internal/command"
)

func NewCompact(deps Deps) command.Handler {
	return command.NewFunc(command.Info{
		Name:        "compact",
		Usage:       "/compact",
		Description: "Compact current session context.",
	}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		content, err := deps.Compact.CompactCurrent(ctx, "manual")
		if err != nil {
			return nil, err
		}
		return &command.Result{Content: content}, nil
	})
}

type CompactModule struct{}

func (CompactModule) RegisterCommands(registrar Registrar, deps Deps) error {
	return RegisterFactories(registrar, deps, NewCompact)
}
