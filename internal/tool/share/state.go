// 定义已发现工具声明、会话工具状态、状态更新与提交结果。
package share

import (
	"elbot/internal/llm"
)

type SourceKind string

const (
	SourceKindNative SourceKind = "native"
	SourceKindELwisp SourceKind = "elwisp"
)

type DiscoveredTool struct {
	Name           string         `json:"name"`                      // 原生工具名或远端调用使用的原始名称。
	CanonicalName  string         `json:"canonical_name,omitempty"`  // 外部工具的模型侧唯一名称；原生工具使用 Name。
	Source         SourceKind     `json:"source"`                    // 声明对应的原生或远端执行来源。
	Description    string         `json:"description,omitempty"`     // 用于展示的工具说明。
	Schema         llm.ToolSchema `json:"schema"`                    // 已提交的模型调用定义，恢复时不查询目录重建。
	EventKey       string         `json:"event_key,omitempty"`       // 远端工具所属的 Elnis 事件标识。
	Endpoint       string         `json:"endpoint,omitempty"`        // 远端工具的调用地址。
	TimeoutSeconds int            `json:"timeout_seconds,omitempty"` // 远端工具声明的调用超时，单位为秒。
	ForegroundOnly bool           `json:"foreground_only,omitempty"` // 标记该声明不可用于后台任务。
}

// State is a detached view of the tool-owned session metadata.
type State struct {
	// DiscoveredTools 是已经提交的完整工具声明，也是会话工具集合的唯一事实。
	DiscoveredTools []DiscoveredTool
	ToolTags        []string // 已提交的工具标签，供提示材料读取。
	// ElyphRulesShown is committed with dialogue messages, never by StateService.
	ElyphRulesShown bool
}

type StateUpdate struct {
	Tools []DiscoveredTool // 待合并的完整声明；同一模型侧名称覆盖已有声明。
	Tags  []string         // 待并入会话的工具标签。
}

type StateCommit struct {
	State    State    // 提交后的工具状态，与输入声明隔离。
	Metadata string   // 提交后的完整 Session 元数据，保留其他模块的字段。
	Injected []string // 本次新增声明的模型侧名称。
	Existing []string // 本次覆盖已有声明的模型侧名称。
}
