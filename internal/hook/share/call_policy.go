// 提供 Hook 工具调用快照和只读策略校验。

package share

import (
	"fmt"
	"reflect"

	"elbot/internal/llm"
)

// SnapshotCalls breaks slice aliases before handing a read-only view to a Hook.
func SnapshotCalls(event Event) Event {
	event.LLM.ToolCalls = append([]llm.ToolCallRequest(nil), event.LLM.ToolCalls...)
	return event
}

// ValidateCalls enforces the explicit caller policy for one handler result.
func ValidateCalls(locked bool, before, after Event) error {
	if !locked {
		return nil
	}
	if !reflect.DeepEqual(before.LLM.ToolCalls, after.LLM.ToolCalls) || before.Tool.ID != after.Tool.ID || before.Tool.Name != after.Tool.Name || before.Tool.Arguments != after.Tool.Arguments {
		return fmt.Errorf("returned tool calls, IDs, names and arguments are read-only for this execution")
	}
	return nil
}
