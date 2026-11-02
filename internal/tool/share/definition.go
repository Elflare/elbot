// 定义工具接口、来源和元数据。
package share

import (
	"context"
	"elbot/internal/llm"
	"fmt"
	"slices"
)

type Source uint8

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
	SourceBuiltin Source = iota + 1
	SourceSkill
	SourceELwisp
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

// Snapshot 只复制元数据中的可变切片。
func (i Info) Snapshot() Info {
	i.APITypes = slices.Clone(i.APITypes)
	i.Tags = slices.Clone(i.Tags)
	i.DependsOn = slices.Clone(i.DependsOn)
	return i
}

// ToolDefinition 是目录唯一的定义来源；文档条目只提供 LoadDetail，不伪造调用入口。
type ToolDefinition struct {
	Metadata         Info                                                      // 工具或文档的目录元数据。
	Parameters       map[string]any                                            // 可调用工具的参数对象 schema；文档条目为 nil。
	Prepare          func(context.Context, CallRequest) (*PreparedCall, error) // 本地实现的准备入口。
	LoadDetail       func(context.Context) (DetailBlock, error)                // 可选的技能正文加载能力。
	ActivateTools    []string                                                  // 加载正文时激活的工具名称。
	DiscoveryContent func(context.Context) (string, bool, error)               // 可选的动态发现说明。
	Remote           *ELwispToolDeclaration                                    // 远端执行所需的可序列化声明；不与 Prepare 同时提供。
}

// ToolRegistrationScope 标识一次完整替换的工具组；远端组仅属于指定会话和事件。
type ToolRegistrationScope struct {
	SessionID string // elwisp 组对应的会话。
	EventKey  string // elwisp 组对应的事件。
}

func (d ToolDefinition) Callable() bool { return d.Prepare != nil || d.Remote != nil }
func (d ToolDefinition) Schema() llm.ToolSchema {
	if !d.Callable() {
		return llm.ToolSchema{}
	}
	return llm.ToolSchema{Name: d.Metadata.Name, Description: d.Metadata.Description, Parameters: d.Parameters}
}
func (d ToolDefinition) PrepareCall(ctx context.Context, req CallRequest) (*PreparedCall, error) {
	if d.Prepare == nil {
		return nil, fmt.Errorf("tool %q has no local execution entry", d.Metadata.Name)
	}
	return d.Prepare(ctx, req)
}

// Snapshot 隔离注册和查询两端的可变声明，执行闭包仍指向同一已选定实现。
func (d ToolDefinition) Snapshot() ToolDefinition {
	d.Metadata = d.Metadata.Snapshot()
	d.ActivateTools = slices.Clone(d.ActivateTools)
	d.Parameters = CloneSchema(llm.ToolSchema{Parameters: d.Parameters}).Parameters
	if d.Remote != nil {
		remote := *d.Remote
		remote.Schema = CloneSchema(llm.ToolSchema{Parameters: remote.Schema}).Parameters
		d.Remote = &remote
	}
	return d
}
