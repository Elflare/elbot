// adapter_api.go 定义可选的原生调用接口，协议与传输归适配器。

package share

import (
	"context"
	"encoding/json"
)

// PlatformAPICaller 执行平台原生 API，不解释或映射通用能力名称。
// 认证、协议封装和传输归适配器。
type PlatformAPICaller interface {
	CallPlatformAPI(ctx context.Context, api string, params map[string]any) (json.RawMessage, error)
}
