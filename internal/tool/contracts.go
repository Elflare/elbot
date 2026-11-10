// 定义 Tool 根部对外交换的数据与回调契约。
package tool

import (
	"context"
	"encoding/json"
	"errors"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/storage"
	"elbot/internal/tool/share"
)

// BackgroundSelection 是授权前展开的工具名单，公开类型不依赖内部 share。
type BackgroundSelection struct {
	Names []string // 选择器展开得到的根工具名称。
	Tag   string   // 使用标签选择时的标签名称。
}

// ToolQuery 描述目录查询；身份、模型和前后台事实由上下文提供。
type ToolQuery struct {
	Scope           QueryScope // 默认筛选当前可用项；QueryCatalog 只查询目录。
	Tag             string     // 展开声明及配置标签，与 Names 取交集；空值不限。
	Kind            string     // tool、skill 或空值（两者）。
	Names           []string   // nil 查询全部，空切片不匹配任何名称。
	IncludeHidden   bool       // 允许查询隐藏项。
	IncludeDiscover bool       // 包含普通查询默认省略的 discover_tool。
}

// QueryScope 区分目录读取与当前上下文可用性，不授予执行权限。
type QueryScope uint8

const (
	QueryAvailable QueryScope = iota
	QueryCatalog
)

// ToolInfo 是只读查询结果；map、slice 与已注册定义共享，不包含执行入口。
type ToolInfo struct {
	Definition Info           // 名称、简介、风险、标签及可用性声明。
	Parameters map[string]any // 参数 schema；纯文档条目为 nil。
}

type Source = share.Source

const (
	SourceBuiltin = share.SourceBuiltin
	SourceSkill   = share.SourceSkill
	SourceELwisp  = share.SourceELwisp
)

// ToolRegistrationScope 仅描述远端定义的会话和事件归属；来源由注册参数统一指定。
type ToolRegistrationScope struct {
	SessionID string // 会话标识；本地注册不填写。
	EventKey  string // 远端事件标识；本地注册不填写。
}

// ToolDefinition 由提供方声明，注册入口统一确定来源与默认值。
type ToolDefinition struct {
	Definition       Info                                                      // 名称、说明、风险及可见性；Source 由注册入口赋值。
	Parameters       map[string]any                                            // 可调用项的参数 schema。
	Prepare          func(context.Context, CallRequest) (*PreparedCall, error) // 本地工具的准备能力。
	LoadDetail       func(context.Context) (DetailBlock, error)                // 可选的文档加载能力。
	ActivateTools    []string                                                  // 展示文档后激活的工具。
	DiscoveryContent func(context.Context) (string, bool, error)               // 动态发现说明及是否替换普通说明。
	Remote           *ELwispToolDeclaration                                    // 远端声明，与本地 Prepare 互斥。
}

type DetailBlock struct {
	Content string `json:"content"` // 文档正文。
	Format  string `json:"format"`  // markdown 或 elyph。
}

type ConfirmationDetailKind = share.ConfirmationDetailKind

const (
	ConfirmationDetailText      = share.ConfirmationDetailText
	ConfirmationDetailDiff      = share.ConfirmationDetailDiff
	ConfirmationDetailArguments = share.ConfirmationDetailArguments
	ConfirmationDetailMarkdown  = share.ConfirmationDetailMarkdown
	ConfirmationDetailCommands  = share.ConfirmationDetailCommands
)

type ConfirmationDetailBlock = share.ConfirmationDetailBlock

var ErrToolDetailNotFound = errors.New("tool detail not found")

// PreparedCall 是外部实现准备好的操作；根部将其封装为固定参数的内部调用。
type PreparedCall struct {
	Risk    RiskAssessment                                 // 本次操作风险。
	Details []ConfirmationDetailBlock                      // 确认预览的有序结构化内容。
	Run     func(context.Context) (*Result, string, error) // 确认后执行的操作。
}

// Info 是公开目录元数据，不携带可执行工具对象。
type Info = share.Info

type SourceKind string

const (
	SourceKindNative SourceKind = "native"
	SourceKindELwisp SourceKind = "elwisp"
)

// DiscoveredTool 是会话已提交的完整声明，恢复时不按名称重建 schema。
type DiscoveredTool struct {
	Risk           security.RiskLevel // 远端接线时确定的风险，不从协议输入。
	Name           string             // 原生名称或远端调用名称。
	CanonicalName  string             // 模型侧名称，为空时使用 Name。
	Source         SourceKind         // 原生或远端来源。
	Description    string             // 模型可见说明。
	Schema         llm.ToolSchema     // 已提交的调用定义。
	EventKey       string             // 远端调用的事件关联标识。
	Endpoint       string             // 远端调用地址。
	TimeoutSeconds int                // 远端调用超时秒数。
	ForegroundOnly bool               // 禁止在后台使用此声明。
}

type State struct {
	DiscoveredTools []DiscoveredTool // 已提交的工具集合。
	ToolTags        []string         // 已提交的提示标签。
	ElyphRulesShown bool             // 通用规则是否已随对话消息提交。
}

type StateUpdate struct {
	ReplaceGroups []ToolRegistrationScope // 随会话事务替换的远端事件组。
	Tools         []DiscoveredTool        // 待合并声明，同名覆盖已有声明。
	Tags          []string                // 待合并标签。
}

type StateCommit struct {
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
	Risk           security.RiskLevel `json:"-"`                         // 接线方固定提供 low。
	CanonicalName  string             `json:"-"`                         // 调用方分配的模型侧唯一名称，不参与外部声明 JSON。
	EventKey       string             `json:"-"`                         // 调用方提供的事件关联标识，不参与外部声明 JSON。
	Name           string             `json:"name"`                      // 远端工具原名。
	Description    string             `json:"description,omitempty"`     // 工具说明。
	Schema         map[string]any     `json:"schema,omitempty"`          // 参数 schema。
	TimeoutSeconds int                `json:"timeout_seconds,omitempty"` // 调用超时秒数。
	Endpoint       string             `json:"endpoint,omitempty"`        // 远端调用地址。
	ForegroundOnly bool               `json:"foreground_only,omitempty"` // 禁止后台使用。
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
	Mode  string            // 会话运行模式。
	Actor contextinfo.Actor // 本次调用者事实。
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
	PrepareToolCall(context.Context, *storage.Session, llm.ToolCallRequest) (llm.ToolCallRequest, error)
	ShouldSendPreview(context.Context, string) bool
	ConfirmToolCall(context.Context, string, llm.ToolCallRequest, RiskAssessment, []ConfirmationDetailBlock) (ConfirmResult, error)
	StartToolRequest(context.Context, string, string) (context.Context, func(), error)
	PrepareToolContext(context.Context, *storage.Session, llm.ToolCallRequest) context.Context
	CompleteToolCall(context.Context, *storage.Session, llm.ToolCallRequest, string, []llm.MessageSegment, error) ([]llm.MessageSegment, error)
	SendPreview(context.Context, ...string)
	SendOutputs(context.Context, []delivery.Output) error
	PublishToolResult(context.Context, string, delivery.ToolResult)
	AuditToolDenied(context.Context, string, llm.ToolCallRequest, security.RiskLevel, string)
	AddToolUse(string, string)
	ToolResultMessage(string, llm.LLMMessage) storage.Message
	ToolCallMessage(string, string, string, string, []llm.ToolCallRequest) storage.Message
}

// ToolCommitter 由调用方实现，同步提交工具结果及其状态补丁，保持对话事务原子性。
type ToolCommitter interface {
	Begin(context.Context, *storage.Message) error
	Prepared(context.Context, int, llm.ToolCallRequest) error
	Result(context.Context, int, llm.ToolCallRequest, llm.LLMMessage, *storage.ToolResultCommit) error
	Flush(context.Context) error
}

type RunRequest struct {
	Committer        ToolCommitter         // 同步持久化能力。
	Session          *storage.Session      // 本轮持有的会话快照，成功提交后同步更新。
	Calls            []llm.ToolCallRequest // 按模型输出顺序排列的调用。
	AssistantText    string                // 模型调用工具时的展示正文。
	AssistantRawText string                // 模型原始正文。
	ReasoningContent string                // 本轮助手思考文本。
	DiscoveredTools  []DiscoveredTool      // 已提交的调用声明。
	Actor            contextinfo.Actor     // 本次批次的身份事实。
}

type RunResult struct {
	Err               error  // 批次执行或提交错误。
	ConfirmationExtra string // 风险确认时的补充输入。
	Stopped           bool   // 批次是否已停止。
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
	Summary         string               // 仅在 LLM 出口前置的摘要，不计入 audit 正文。
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

// ErrSkillNotFound 表示外部 Skill 管理目标不存在。
var ErrSkillNotFound = share.ErrSkillNotFound
