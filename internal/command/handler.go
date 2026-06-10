package command

import "context"

type FuncHandler struct {
	info Info
	fn   func(context.Context, Request) (*Result, error)
}

func NewFunc(info Info, fn func(context.Context, Request) (*Result, error)) *FuncHandler {
	return &FuncHandler{info: info, fn: fn}
}

func (h *FuncHandler) Info() Info {
	return h.info
}

func (h *FuncHandler) Handle(ctx context.Context, req Request) (*Result, error) {
	return h.fn(ctx, req)
}
