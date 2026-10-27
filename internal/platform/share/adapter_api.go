// adapter_api.go 定义通用能力和原生调用的内部接口，不包含平台映射实现。

package share

import (
	"context"
	"encoding/json"
)

// CapabilityCaller 执行已支持的通用能力；未知能力不回退为原生 API。
// 平台选择归路由组件，能力名称到协议请求的映射归适配器。
type CapabilityCaller interface {
	CallCapability(ctx context.Context, name string, params map[string]any) (json.RawMessage, error)
}

// PlatformAPICaller 执行平台原生 API，不解释或映射通用能力名称。
// 认证、协议封装和传输归适配器。
type PlatformAPICaller interface {
	CallPlatformAPI(ctx context.Context, api string, params map[string]any) (json.RawMessage, error)
}
