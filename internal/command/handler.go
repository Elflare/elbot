package command

import (
	"context"

	"elbot/internal/command/share"
)

type FuncHandler = share.FuncHandler

func NewFunc(info Info, fn func(context.Context, Request) (*Result, error)) *FuncHandler {
	return share.NewFunc(info, fn)
}
