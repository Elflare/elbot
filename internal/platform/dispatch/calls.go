// calls.go 按显式平台转发原生 API；接口名、参数和结果的解释归适配器。
package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"elbot/internal/platform/share"
)

// CallPlatformAPI 只查找指定平台，不回退到当前会话或默认发送平台。
func (r *Router) CallPlatformAPI(ctx context.Context, platform, api string, params map[string]any) (json.RawMessage, error) {
	platform = strings.TrimSpace(platform)
	r.mu.RLock()
	sender := r.senders[platform]
	r.mu.RUnlock()
	if sender == nil {
		return nil, fmt.Errorf("target platform %q is not configured", platform)
	}
	caller, ok := sender.(share.PlatformAPICaller)
	if !ok {
		return nil, fmt.Errorf("platform %q does not support api calls", platform)
	}
	return caller.CallPlatformAPI(ctx, api, params)
}
