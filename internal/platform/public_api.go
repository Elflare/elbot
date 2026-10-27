// public_api.go 提供对外通用能力调用与原生 API 透传入口。

package platform

import (
	"context"
	"encoding/json"
)

// CallCapability 只调用 ElBot 明确实现的通用能力；未知能力不转入原生透传。
func (m *Supervisor) CallCapability(ctx context.Context, platform, name string, params map[string]any) (json.RawMessage, error) {
	return m.dispatcher.CallCapability(ctx, platform, name, params)
}

// CallPlatformAPI 转交平台原生接口名称与参数，不识别、映射或登记具体接口。
func (m *Supervisor) CallPlatformAPI(ctx context.Context, platform, api string, params map[string]any) (json.RawMessage, error) {
	return m.dispatcher.CallPlatformAPI(ctx, platform, api, params)
}
