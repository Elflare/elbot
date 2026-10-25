// 评估已选定工具的风险，并取得调用准备快照。
package execution

import (
	"context"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/tool/share"
	"encoding/json"
)

func (m *Manager) assessRisk(ctx context.Context, resolved resolvedTool, call llm.ToolCallRequest) (share.RiskAssessment, error) {
	if resolved.Source == share.SourceKindELwisp {
		return share.RiskAssessment{Level: share.RiskLow}, nil
	}
	fallback := security.ParseRisk(string(resolved.Native.Info().Risk), share.RiskHigh)
	if assessor, ok := resolved.Native.(share.RiskAssessor); ok {
		assessment, err := assessor.AssessRisk(ctx, share.CallRequest{ID: call.ID, Name: call.Name, Arguments: json.RawMessage(call.Arguments)})
		assessment.Level = security.ParseRisk(string(assessment.Level), fallback)
		return assessment, err
	}
	return share.RiskAssessment{Level: fallback}, nil
}

func (m *Manager) prepareCall(ctx context.Context, resolved resolvedTool, call llm.ToolCallRequest) (*share.PreparedCall, error) {
	if preparer, ok := resolved.Native.(share.CallPreparer); ok {
		return preparer.PrepareCall(ctx, share.CallRequest{ID: call.ID, Name: call.Name, Arguments: json.RawMessage(call.Arguments)})
	}
	return nil, nil
}
