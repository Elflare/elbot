// 提供 Hook 执行前的工具调用快照。

package share

import (
	"elbot/internal/llm"
)

// SnapshotCalls breaks slice aliases before handing a read-only view to a Hook.
func SnapshotCalls(event Event) Event {
	event.LLM.ToolCalls = append([]llm.ToolCallRequest(nil), event.LLM.ToolCalls...)
	return event
}
