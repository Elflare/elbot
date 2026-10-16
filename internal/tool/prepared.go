package tool

import (
	"bytes"
	"context"
)

// CallPreparer validates before confirmation; nil means ordinary Call execution.
type CallPreparer interface {
	PrepareCall(context.Context, CallRequest) (*PreparedCall, error)
}

// PreparedCall owns a confirmation snapshot for one serial invocation.
// It retains neither an execution context nor locks.
type PreparedCall struct {
	// tool owns both preparation and execution.
	tool Tool
	// request binds the snapshot to independent invocation arguments.
	request CallRequest
	// detail is the confirmation text produced by preparation.
	detail string
	// run consumes the tool-specific snapshot with the execution context.
	run func(context.Context) (*Result, string, error)
}

func NewPreparedCall(candidate Tool, req CallRequest, detail string, run func(context.Context) (*Result, string, error)) *PreparedCall {
	req.Arguments = bytes.Clone(req.Arguments)
	return &PreparedCall{tool: candidate, request: req, detail: detail, run: run}
}

func PrepareCall(ctx context.Context, candidate Tool, req CallRequest) (*PreparedCall, error) {
	if preparer, ok := candidate.(CallPreparer); ok {
		return preparer.PrepareCall(ctx, req)
	}
	return nil, nil
}

func (p *PreparedCall) RiskDetail() string { return p.detail }

func (p *PreparedCall) Call(ctx context.Context) (*Result, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err.Error(), err
	}
	return p.run(ctx)
}

func (p *PreparedCall) matches(req CallRequest) bool {
	return p.request.ID == req.ID && p.request.Name == req.Name && bytes.Equal(p.request.Arguments, req.Arguments)
}
