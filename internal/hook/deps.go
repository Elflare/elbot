// 声明 Hook 总管的构造依赖与 Tool、Platform 等外部消费能力。

package hook

import (
	"context"
	"encoding/json"

	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/tool"
)

// SupervisorDeps contains borrowed capabilities. Hook neither owns their
// lifecycle nor obtains their registries, adapters or storage implementations.
type SupervisorDeps struct {
	Media MediaAPI // Shared media center; runtime owns only its own media leases.
	Tools interface {
		ToolInfo(string) (tool.Info, bool)
		ToolSchema(string) (llm.ToolSchema, bool)
		CallTool(context.Context, tool.CallRequest) (*tool.Result, string, error)
	} // Tool metadata, schema and invocation, without exposing callable Tool objects.
	Platform interface {
		SendTo(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, events.LogResult, error)
		CallPlatformAPI(context.Context, string, string, map[string]any) (json.RawMessage, error)
	} // Delivery and named-platform API calls; platform adapters stay private.
}
