// 定义工具接口、来源和元数据。
package share

import (
	"context"
	"elbot/internal/llm"
)

type Source string

// ToolQuery selects catalog entries; identity, model and background facts come from context.
type ToolQuery struct {
	// Kind is "tool" for plain tools, "skill" for detail providers, or empty for both.
	Kind string
	// Names restricts exact names; nil selects the full catalog and an empty slice selects none.
	Names []string
	// IncludeHidden permits internal dependency resolution to include hidden entries.
	IncludeHidden bool
	// IncludeDiscover includes discover_tool, which ordinary completion and preloading omit.
	IncludeDiscover bool
}

const (
	SourceBuiltin    Source = "builtin"
	SourceSkillAgent Source = "skill_agent"
	SourceSkillGo    Source = "skill_go"
)

type Info struct {
	// NeedsElyphRules requests the shared rules when this tool is discovered.
	NeedsElyphRules bool
	// APITypes restricts model API availability; empty permits every API type.
	APITypes       []llm.APIType
	Name           string
	Description    string
	Source         Source
	Risk           RiskLevel
	SuperadminOnly bool
	// Hidden controls prompt/list exposure only. It is not a security boundary.
	Hidden bool
	// OwnerScoped marks tools that only read/write the calling actor's own data.
	// When true, Permission.CanUseTool allows regular users regardless of risk level.
	// The tool handler is responsible for scoping access by actor.
	OwnerScoped bool
	// ForegroundOnly marks tools that are only available in foreground sessions.
	ForegroundOnly bool
	// Tags are user-facing grouping labels for completion and manual preloading.
	// They are not a security boundary and are not exposed through discover_tool.
	Tags      []string
	DependsOn []string
}

type Tool interface {
	Name() string
	Info() Info
	Schema() llm.ToolSchema
	// Call returns the business result, an independent log body, and the original
	// business error. The runner declares the invocation log status and level
	// from the execution outcome; tools supply logContent, including diagnostics.
	// Tools summarize and truncate their own log bodies to 1024 runes,
	// including the ellipsis. Business results remain unchanged.
	Call(ctx context.Context, req CallRequest) (result *Result, logContent string, callErr error)
}
