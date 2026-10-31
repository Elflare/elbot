// 保存单次工具调用的参数、风险、预览及执行入口。
package share

import (
	"bytes"
	"context"
)

// PreparedCall 保存一次调用的完整准备结果，不持有准备阶段的 context 或锁。
// 工具的执行回调应使用已固定的业务数据和 Call 传入的执行 context。
type PreparedCall struct {
	// request 保存独立的调用参数，执行前用来检查调用身份与参数是否变化。
	request CallRequest
	// assessment 保存本次准备时确定的完整风险结果，不在确认或执行时重新评估。
	assessment RiskAssessment
	// preview 保存确认展示内容；自动确认只跳过展示，不跳过准备。
	preview string
	// run 使用准备好的业务数据执行，返回业务结果、未经截断的独立摘要和原始错误。
	run func(context.Context) (*Result, string, error)
}

func NewPreparedCall(req CallRequest, assessment RiskAssessment, preview string, run func(context.Context) (*Result, string, error)) *PreparedCall {
	req.Arguments = bytes.Clone(req.Arguments)
	assessment.Reasons = append([]string(nil), assessment.Reasons...)
	return &PreparedCall{request: req, assessment: assessment, preview: preview, run: run}
}

func (p *PreparedCall) Preview() string { return p.preview }

func (p *PreparedCall) RiskAssessment() RiskAssessment {
	assessment := p.assessment
	assessment.Reasons = append([]string(nil), assessment.Reasons...)
	return assessment
}

// Call executes the prepared operation and preserves its result, untruncated
// independent summary and original error. Audit result previews are separate.
func (p *PreparedCall) Call(ctx context.Context) (*Result, string, error) {
	return p.run(ctx)
}

func (p *PreparedCall) Matches(req CallRequest) bool {
	return p.request.ID == req.ID && p.request.Name == req.Name && bytes.Equal(p.request.Arguments, req.Arguments)
}
