// 声明 Hook 总管的构造依赖与 Tool、Platform 等外部消费能力。

package hook

import (
	"elbot/internal/operationresult"
	"elbot/internal/tool"
)

import (
	"context"
	"elbot/internal/control"
	"encoding/json"

	"elbot/internal/delivery"
)

// SupervisorDeps contains borrowed capabilities. Hook neither owns their
// lifecycle nor obtains their registries, adapters or storage implementations.
type SupervisorDeps struct {
	Control *control.Supervisor // App 创建的统一操作入口，仅借用构造接线能力。
	Media   MediaAPI            // Shared media center; runtime owns only its own media leases.
	Tools   interface {
		QueryTools(context.Context, tool.ToolQuery) ([]tool.ToolInfo, error)
		CallTool(context.Context, tool.CallRequest) (operationresult.Result[*tool.Result], string, error)
	} // Tool metadata, schema and invocation, without exposing callable Tool objects.
	Platform interface {
		SendTo(context.Context, delivery.Target, []delivery.Output) (operationresult.Result[delivery.Receipt], error)
		CallPlatformAPI(context.Context, string, string, map[string]any) (json.RawMessage, error)
	} // Delivery and named-platform API calls; platform adapters stay private.
}
