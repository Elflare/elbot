// 定义 Tool 根部对外交换的数据与回调契约。
package tool

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/storage"
	"encoding/json"
	"time"
)

// BackgroundSelection 是授权前展开的工具名单，公开类型不依赖内部 share。
type BackgroundSelection struct {
	Names []string // 选择器展开得到的根工具名称。
	Tag   string   // 使用标签选择时的标签名称。
}

// ToolQuery 描述目录查询；身份、模型和前后台事实由上下文提供。
type ToolQuery struct {
	Kind            string   // tool、skill 或空值（两者）。
	Names           []string // nil 查询全部，空切片不匹配任何名称。
	IncludeHidden   bool     // 允许查询隐藏项。
	IncludeDiscover bool     // 包含普通查询默认省略的 discover_tool。
}

type Source string

const (
	SourceBuiltin    Source = "builtin"
	SourceSkillAgent Source = "skill_agent"
	SourceSkillGo    Source = "skill_go"
)

// Info 是公开目录元数据，不携带可执行工具对象。
type Info struct {
	NeedsElyphRules bool               // 展示该工具时需要 ELyph 通用规则。
	APITypes        []llm.APIType      // 限定可用协议，空值不限。
	Name            string             // 工具名称。
	Description     string             // 面向使用方的说明。
	Source          Source             // 注册来源。
	Risk            security.RiskLevel // 静态风险提示，最终风险在准备调用时确定。
	SuperadminOnly  bool               // 仅超级管理员可用。
	Hidden          bool               // 默认隐藏于目录和提示材料。
	OwnerScoped     bool               // 工具实现保证只访问调用者的数据。
	ForegroundOnly  bool               // 仅可在前台使用。
	Tags            []string           // 查询和展示分组。
	DependsOn       []string           // 发现时需同时提供的依赖名称。
}

type SourceKind string

const (
	SourceKindNative SourceKind = "native"
	SourceKindELwisp SourceKind = "elwisp"
)

// DiscoveredTool 是会话已提交的完整声明，恢复时不按名称重建 schema。
type DiscoveredTool struct {
	Name           string         // 原生名称或远端调用名称。
	CanonicalName  string         // 模型侧名称，为空时使用 Name。
	Source         SourceKind     // 原生或远端来源。
	Description    string         // 模型可见说明。
	Schema         llm.ToolSchema // 已提交的调用定义。
	EventKey       string         // 远端调用的事件关联标识。
	Endpoint       string         // 远端调用地址。
	TimeoutSeconds int            // 远端调用超时秒数。
	ForegroundOnly bool           // 禁止在后台使用此声明。
}

type State struct {
	DiscoveredTools []DiscoveredTool // 已提交的工具集合。
	ToolTags        []string         // 已提交的提示标签。
	ElyphRulesShown bool             // 通用规则是否已随对话消息提交。
}

type StateUpdate struct {
	Tools []DiscoveredTool // 待合并声明，同名覆盖已有声明。
	Tags  []string         // 待合并标签。
}

type StateCommit struct {
	State    State    // 提交后的工具状态。
	Metadata string   // 保留其他业务字段的完整会话元数据，用于同步调用方快照。
	Injected []string // 本次新增的模型侧名称。
	Existing []string // 本次更新的已有模型侧名称。
}

type ToolPreload struct {
	NeedsElyphRules bool        // 材料需要 ELyph 通用规则。
	Update          StateUpdate // 准备完成但尚未提交的状态。
	Accepted        []bool      // 按输入顺序标记已接纳的名称。
	Invalid         []string    // 未找到或不可用的名称。
	Content         string      // 工具说明材料。
}

type SkillPreload struct {
	NeedsElyphRules bool        // 材料需要 ELyph 通用规则。
	Update          StateUpdate // Skill 激活的工具声明，尚未提交。
	Accepted        []bool      // 按输入顺序标记已接纳的名称。
	Names           []string    // 已读取的 Skill 名称。
	Invalid         []string    // 未找到或不可用的名称。
	Content         string      // Skill 文档及工具说明。
}

// ELwispToolDeclaration 是已授权的远端工具声明；不携带事件业务对象。
type ELwispToolDeclaration struct {
	CanonicalName  string         `json:"-"`                         // 调用方分配的模型侧唯一名称，不参与外部声明 JSON。
	EventKey       string         `json:"-"`                         // 调用方提供的事件关联标识，不参与外部声明 JSON。
	Name           string         `json:"name"`                      // 远端工具原名。
	Description    string         `json:"description,omitempty"`     // 工具说明。
	Schema         map[string]any `json:"schema,omitempty"`          // 参数 schema。
	TimeoutSeconds int            `json:"timeout_seconds,omitempty"` // 调用超时秒数。
	Endpoint       string         `json:"endpoint,omitempty"`        // 远端调用地址。
	ForegroundOnly bool           `json:"foreground_only,omitempty"` // 禁止后台使用。
}

type BackgroundPreloadRequest struct {
	SessionID        string                  // 预载审计关联会话。
	Names            []string                // 本地工具、Skill 或标签名称。
	AllowedToolNames []string                // nil 不额外限制，空切片不允许本地根工具。
	ELwispTools      []ELwispToolDeclaration // 已授权的外部声明。
}

type BackgroundPreload struct {
	NeedsElyphRules bool        // 材料需要 ELyph 通用规则。
	Update          StateUpdate // 尚未提交的完整声明与标签。
	Skills          []string    // 已读取的 Skill 名称。
	SkillPrompt     string      // 后台输入所需的工具说明与 Skill 文档。
}

type TagPrompt struct {
	Tag    string // 所属标签。
	Prompt string // 对应提示材料。
}

type Context struct {
	Mode             string            // 会话运行模式。
	Actor            contextinfo.Actor // 本次调用者事实。
	DisableBaseTools bool              // 只使用已发现声明，不注入基础工具。
}

type RiskAssessment struct {
	Level   security.RiskLevel // 准备调用时确定的完整风险等级。
	Reasons []string           // 风险判断说明。
}

type ConfirmResult struct {
	Allowed bool           // 是否批准本次调用。
	Extra   string         // 用户补充输入。
	Message llm.LLMMessage // 拒绝或停止时应提交的工具结果。
	Stopped bool           // 是否停止后续批次处理。
}

// RunnerDeps 是批次执行期间向调用方请求的能力；只在该次 Run 中借用。
type RunnerDeps interface {
	RefreshExecution(context.Context, *storage.Session) (context.Context, error)
	PrepareToolCall(context.Context, *storage.Session, llm.ToolCallRequest) (llm.ToolCallRequest, events.LogResult, error)
	ShouldSendPreview(context.Context, *storage.Session, llm.ToolCallRequest, string) bool
	ConfirmToolCall(context.Context, string, llm.ToolCallRequest, RiskAssessment, string) (ConfirmResult, error)
	StartToolRequest(context.Context, string, string) (context.Context, time.Time, func(), events.LogResult, error)
	PrepareToolContext(context.Context, *storage.Session, llm.ToolCallRequest) context.Context
	CompleteToolCall(context.Context, *storage.Session, llm.ToolCallRequest, string, []llm.MessageSegment, error) ([]llm.MessageSegment, events.LogResult, error)
	SendPreview(context.Context, ...string)
	SendOutputs(context.Context, []delivery.Output) (events.LogResult, error)
	AuditToolDenied(context.Context, string, llm.ToolCallRequest, security.RiskLevel, string)
	AddToolUse(string, string)
	ToolResultMessage(string, llm.LLMMessage) storage.Message
	ToolCallMessage(string, string, string, []llm.ToolCallRequest) storage.Message
}

// ToolCommitter 由调用方实现，同步提交工具结果及其状态补丁，保持对话事务原子性。
type ToolCommitter interface {
	Begin(context.Context, *storage.Message) (events.LogResult, error)
	Prepared(context.Context, int, llm.ToolCallRequest) (events.LogResult, error)
	Result(context.Context, int, llm.ToolCallRequest, llm.LLMMessage, *storage.ToolResultCommit) (events.LogResult, error)
	Flush(context.Context) (events.LogResult, error)
}

type RunRequest struct {
	Committer        ToolCommitter         // 同步持久化能力。
	Session          *storage.Session      // 本轮持有的会话快照，成功提交后同步更新。
	Calls            []llm.ToolCallRequest // 按模型输出顺序排列的调用。
	AssistantText    string                // 模型调用工具时的展示正文。
	AssistantRawText string                // 模型原始正文。
	DiscoveredTools  []DiscoveredTool      // 已提交的调用声明。
	Actor            contextinfo.Actor     // 本次批次的身份事实。
}

type RunResult struct {
	Err               error                 // 批次执行或提交错误。
	Messages          []llm.LLMMessage      // 已生成的工具结果消息。
	PreparedCalls     []llm.ToolCallRequest // Hook 准备后的实际调用。
	ConfirmationExtra string                // 风险确认时的补充输入。
	Stopped           bool                  // 批次是否已停止。
}

type CallRequest struct {
	ID        string          // 调用关联标识。
	Name      string          // 原生工具名称。
	Arguments json.RawMessage // 已授权的调用参数。
}

type DiscoveryText struct {
	Summary string // 发现摘要。
	Details string // 发现详情。
}

type Result struct {
	NeedsElyphRules bool                 // 展示结果时需要 ELyph 规则。
	DiscoveryText   *DiscoveryText       // 发现摘要与详情的展示边界。
	Content         string               // 文本结果。
	Segments        []llm.MessageSegment // 多模态结果，优先于 Content。
	Warnings        []string             // 附加给模型的使用提示。
	Data            json.RawMessage      // 发现结果的结构化数据。
	Metadata        map[string]any       // 工具附加事实。
	Outputs         []delivery.Output    // 需要发送给用户的内容。
}

func (r *Result) LLMSegments() []llm.MessageSegment { return toInternalResult(r).LLMSegments() }

// LogContent returns the actual result/error preview for audit, capped at 1024
// Unicode characters. The separately returned tool summary remains untruncated.
func (r *Result) LogContent(err error) string { return toInternalResult(r).LogContent(err) }
