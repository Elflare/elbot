// 定义工具接口、来源和元数据。
package share

import (
	"context"
	"elbot/internal/delivery"
	"elbot/internal/llm"
	"errors"
	"fmt"
	"slices"
	"strings"
)

type Source uint8

// ToolQuery selects catalog entries; identity, model and background facts come from context.
type ToolQuery struct {
	Scope QueryScope // QueryAvailable 筛选运行资格，QueryCatalog 只读取目录。
	// Kind is "tool" for plain tools, "skill" for detail providers, or empty for both.
	Kind string
	// Names restricts exact names; nil selects the full catalog and an empty slice selects none.
	Names []string
	// IncludeHidden permits internal dependency resolution to include hidden entries.
	IncludeHidden bool
	// IncludeDiscover includes discover_tool, which ordinary completion and preloading omit.
	IncludeDiscover bool
}

// QueryScope 是统一目录筛选的范围。
type QueryScope uint8

const (
	QueryAvailable QueryScope = iota
	QueryCatalog
)

const (
	SourceBuiltin Source = iota + 1
	SourceSkill
	SourceELwisp
)

// Info 是工具目录元数据，不携带可执行工具对象。
type Info struct {
	NeedsElyphRules bool          // 发现该工具时需要 ELyph 通用规则。
	APITypes        []llm.APIType // 限定可用协议，空值不限。
	Name            string        // 工具名称。
	Description     string        // 面向使用方的说明。
	Source          Source        // 注册来源，由注册入口的来源参数统一赋值。
	Risk            RiskLevel     // 静态风险提示，最终风险在准备调用时确定。
	SuperadminOnly  bool          // 仅超级管理员可用。
	Hidden          bool          // 默认隐藏于目录和提示材料，不构成权限边界。
	// OwnerScoped 由工具实现保证只访问调用者的数据；
	// Permission.CanUseTool 据此允许普通用户使用，不受静态风险等级限制。
	OwnerScoped    bool
	ForegroundOnly bool // 仅可在前台使用。
	// Tags 用于补全和手动预载分组，不构成权限边界，也不通过 discover_tool 展示。
	Tags      []string
	DependsOn []string // 发现时需同时提供的依赖名称。
}

// ToolDefinition 是目录唯一的定义来源；文档条目只提供 LoadDetail，不伪造调用入口。
type ToolDefinition struct {
	Definition       Info                                                      // 工具或文档的目录元数据。
	Parameters       map[string]any                                            // 可调用工具的参数对象 schema；文档条目为 nil。
	Prepare          func(context.Context, CallRequest) (*PreparedCall, error) // 本地实现的准备入口。
	LoadDetail       func(context.Context) (DetailBlock, error)                // 可选的技能正文加载能力。
	ActivateTools    []string                                                  // 加载正文时激活的工具名称。
	DiscoveryContent func(context.Context) (string, bool, error)               // 可选的动态发现说明。
	Remote           *ELwispToolDeclaration                                    // 远端执行所需的可序列化声明；不与 Prepare 同时提供。
}

type ConfirmationDetailKind = delivery.ContentBlockKind

const (
	ConfirmationDetailText      = delivery.ContentText
	ConfirmationDetailDiff      = delivery.ContentDiff
	ConfirmationDetailArguments = delivery.ContentArguments
	ConfirmationDetailMarkdown  = delivery.ContentMarkdown
)

type ConfirmationDetailBlock = delivery.ContentBlock

func TextConfirmationDetail(content string) ConfirmationDetailBlock {
	return ConfirmationDetailBlock{Kind: ConfirmationDetailText, Content: content}
}

// FileDiffConfirmationDetail returns a diff block when complete, otherwise its text fallback.
func FileDiffConfirmationDetail(diff string, available bool, fallback string) ConfirmationDetailBlock {
	if available {
		return ConfirmationDetailBlock{Kind: ConfirmationDetailDiff, Content: diff}
	}
	return TextConfirmationDetail(fallback)
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
	return llm.ToolSchema{Name: d.Definition.Name, Description: d.Definition.Description, Parameters: d.Parameters}
}
func (d ToolDefinition) PrepareCall(ctx context.Context, req CallRequest) (*PreparedCall, error) {
	if d.Prepare == nil {
		return nil, fmt.Errorf("tool %q has no local execution entry", d.Definition.Name)
	}
	return d.Prepare(ctx, req)
}

// Snapshot 在注册时隔离输入；发布后的 map、slice 只读共享，重载替换整个定义。
func (d ToolDefinition) Snapshot() ToolDefinition {
	d.Definition.APITypes = slices.Clone(d.Definition.APITypes)
	d.Definition.Tags = slices.Clone(d.Definition.Tags)
	d.Definition.DependsOn = slices.Clone(d.Definition.DependsOn)
	d.ActivateTools = slices.Clone(d.ActivateTools)
	d.Parameters = CloneSchema(llm.ToolSchema{Parameters: d.Parameters}).Parameters
	if d.Remote != nil {
		remote := *d.Remote
		remote.Schema = d.Parameters
		d.Remote = &remote
	}
	return d
}

// NormalizeToolTag 规范化标签；空值或包含非法字符时返回空字符串。
func NormalizeToolTag(tag string) string {
	tag = strings.ToLower(strings.TrimSpace(tag))
	for i := 0; i < len(tag); i++ {
		c := tag[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.') {
			return ""
		}
	}
	return tag
}

// ErrSkillNotFound 表示待管理的外部 Skill 未注册。
var ErrSkillNotFound = errors.New("Skill 不存在")
