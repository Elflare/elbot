// 为已选定的原生或远程工具取得完整调用准备结果。
package execution

import (
	"context"
	"elbot/internal/llm"
	"elbot/internal/tool/share"
	"encoding/json"
)

func (m *Manager) prepareCall(ctx context.Context, resolved resolvedTool, call llm.ToolCallRequest) (*share.PreparedCall, error) {
	if resolved.Source == share.SourceKindELwisp {
		return prepareELwispTool(call, resolved)
	}
	return resolved.Native.PrepareCall(ctx, share.CallRequest{ID: call.ID, Name: call.Name, Arguments: json.RawMessage(call.Arguments)})
}
