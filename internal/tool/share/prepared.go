// 定义工具准备契约及单次调用的不可变参数快照。
package share

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
	// request binds the snapshot to independent invocation arguments.
	request CallRequest
	// detail is the confirmation text produced by preparation.
	detail string
	// run consumes the tool-specific snapshot with the execution context.
	run func(context.Context) (*Result, string, error)
}

func NewPreparedCall(req CallRequest, detail string, run func(context.Context) (*Result, string, error)) *PreparedCall {
	req.Arguments = bytes.Clone(req.Arguments)
	return &PreparedCall{request: req, detail: detail, run: run}
}

func (p *PreparedCall) RiskDetail() string { return p.detail }

func (p *PreparedCall) Call(ctx context.Context) (*Result, string, error) {
	return p.run(ctx)
}

func (p *PreparedCall) Matches(req CallRequest) bool {
	return p.request.ID == req.ID && p.request.Name == req.Name && bytes.Equal(p.request.Arguments, req.Arguments)
}
