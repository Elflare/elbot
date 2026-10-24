// 管理调用准备和确认快照，承接准备后的执行。
package execution

import toolshare "elbot/internal/tool/share"

import (
	"bytes"
	"context"
)

// CallPreparer validates before confirmation; nil means ordinary Call execution.
type CallPreparer interface {
	PrepareCall(context.Context, toolshare.CallRequest) (*PreparedCall, error)
}

// PreparedCall owns a confirmation snapshot for one serial invocation.
// It retains neither an execution context nor locks.
type PreparedCall struct {
	// tool owns both preparation and execution.
	tool toolshare.Tool
	// request binds the snapshot to independent invocation arguments.
	request toolshare.CallRequest
	// detail is the confirmation text produced by preparation.
	detail string
	// run consumes the tool-specific snapshot with the execution context.
	run func(context.Context) (*toolshare.Result, string, error)
}

func NewPreparedCall(candidate toolshare.Tool, req toolshare.CallRequest, detail string, run func(context.Context) (*toolshare.Result, string, error)) *PreparedCall {
	req.Arguments = bytes.Clone(req.Arguments)
	return &PreparedCall{tool: candidate, request: req, detail: detail, run: run}
}

func PrepareCall(ctx context.Context, candidate toolshare.Tool, req toolshare.CallRequest) (*PreparedCall, error) {
	if preparer, ok := candidate.(CallPreparer); ok {
		return preparer.PrepareCall(ctx, req)
	}
	return nil, nil
}

func (p *PreparedCall) RiskDetail() string { return p.detail }

func (p *PreparedCall) Call(ctx context.Context) (*toolshare.Result, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err.Error(), err
	}
	return p.run(ctx)
}

func (p *PreparedCall) matches(req toolshare.CallRequest) bool {
	return p.request.ID == req.ID && p.request.Name == req.Name && bytes.Equal(p.request.Arguments, req.Arguments)
}
