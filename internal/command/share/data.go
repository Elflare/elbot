// data.go 定义命令出口的业务数据；展示文字不能替代这些字段。
package share

import (
	"elbot/internal/config"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/request"
	"elbot/internal/security"
	"elbot/internal/turn"
	"encoding/json"
	"time"
)

// Page 保留当前页定位；TotalPages 仅在能够取得总页数的查询中提供。
type Page struct {
	Page       int  `json:"page"`                  // 从 1 开始的页码。
	PageSize   int  `json:"page_size"`             // 每页容量。
	HasNext    bool `json:"has_next"`              // 是否还有下一页。
	TotalPages *int `json:"total_pages,omitempty"` // 已知总页数，空历史为 0。
}
type SessionData struct {
	ID                string     `json:"id"`                   // 会话标识。
	Title             string     `json:"title"`                // 原始标题。
	Mode              string     `json:"mode"`                 // 会话运行模式。
	Status            string     `json:"status"`               // 持久化状态。
	Platform          string     `json:"platform"`             // 所属平台。
	ChatID            string     `json:"chat_id"`              // 所属平台会话。
	CreatedAt         time.Time  `json:"created_at"`           // 创建时间。
	UpdatedAt         time.Time  `json:"updated_at"`           // 最后更新时间。
	ArchivedAt        *time.Time `json:"archived_at"`          // 归档时间，nil 表示未归档。
	PinnedAt          *time.Time `json:"pinned_at"`            // 置顶时间，nil 表示未置顶。
	ParentSessionID   string     `json:"parent_session_id"`    // 派生会话的父会话；非派生会话为空。
	ForkFromMessageID string     `json:"fork_from_message_id"` // 分支起点消息；非分支会话为空。
}
type SessionSummaryData struct {
	SessionData
	MessageCount         int           `json:"message_count"`          // 会话消息数量。
	Messages             []MessageData `json:"messages"`               // 最近原始对话消息。
	LastUserContent      string        `json:"last_user_content"`      // 最近用户正文。
	LastAssistantContent string        `json:"last_assistant_content"` // 最近助手正文。
}
type SessionsData struct {
	Page
	Items            []SessionSummaryData `json:"items"`              // 当前页可访问的会话摘要。
	CurrentSessionID string               `json:"current_session_id"` // 当前绑定，空字符串表示未绑定。
	Query            string               `json:"query"`              // 本次搜索词。
}
type SessionChangeData struct {
	Session          *SessionData  `json:"session"`            // 目标会话快照；确认前保留原状态，解除绑定或删除后为 nil。
	SessionID        string        `json:"session_id"`         // 操作目标；批量清理不填写。
	CurrentSessionID string        `json:"current_session_id"` // 返回时的当前绑定，空字符串表示未绑定。
	AffectedCount    int           `json:"affected_count"`     // 实际变更数量。
	Messages         []MessageData `json:"messages"`           // resume/fork 已读取的最近消息。
}
type AttachmentData struct {
	Name string `json:"name"` // 历史附件展示名称，不携带存储位置或授权。
}
type MessageData struct {
	ID          string           `json:"id"`          // 持久化消息标识。
	Role        llm.MessageRole  `json:"role"`        // 原始用户或助手角色。
	Content     string           `json:"content"`     // 完整正文，保留空白及换行。
	Attachments []AttachmentData `json:"attachments"` // 按片段顺序排列的附件。
	CreatedAt   time.Time        `json:"created_at"`  // 原始创建时间。
}
type MessagesData struct {
	Page
	SessionID string        `json:"session_id"` // 本次读取的当前会话。
	Items     []MessageData `json:"items"`      // 页内按对话顺序排列；第 1 页是最新页。
}
type ModelData struct {
	Provider string   `json:"provider"` // 提供方名称。
	Model    string   `json:"model"`    // 模型名称。
	Index    int      `json:"index"`    // 目录选择编号；非目录项为 0。
	Modes    []string `json:"modes"`    // 当前使用此模型的模式。
	Compact  bool     `json:"compact"`  // 是否选为压缩模型。
	Naming   bool     `json:"naming"`   // 是否选为命名模型。
}
type ModelSelectionData struct {
	Target string    `json:"target"` // current 表示当前／默认模式，其余为显式模式或 compact/naming 目标。
	Model  ModelData `json:"model"`  // 实际提交的模型。
}
type ItemError struct {
	ID    string `json:"id"`    // 失败项的业务标识。
	Error *Error `json:"error"` // 此项的分类错误。
}
type ModelsData struct {
	Items  []ModelData `json:"items"`  // 查询匹配的模型。
	Errors []ItemError `json:"errors"` // 各提供方的失败，不覆盖成功项。
}
type ContextData struct {
	Usage               *TokenUsage `json:"usage"`                 // 原始计数，nil 或可空计数字段表示未知。
	ContextWindow       int         `json:"context_window"`        // 当前模型上下文容量。
	CompactEnabled      bool        `json:"compact_enabled"`       // 是否启用自动压缩。
	CompactTriggerRatio float64     `json:"compact_trigger_ratio"` // 自动压缩触发比例。
}
type RequestData struct {
	ID        string       `json:"id"`         // 请求标识。
	ParentID  string       `json:"parent_id"`  // 父请求标识。
	SessionID string       `json:"session_id"` // 所属会话。
	Kind      request.Kind `json:"kind"`       // 请求业务类型。
	Label     string       `json:"label"`      // 原始说明。
	StartedAt time.Time    `json:"started_at"` // 开始时间。
	Stopping  bool         `json:"stopping"`   // 已接受停止，尚未退出。
}
type RequestsData struct {
	Items []RequestData `json:"items"` // 请求快照。
}
type StatusData struct {
	Session              *SessionData   `json:"session"`                // 当前会话，尚未开始时为 nil。
	Mode                 string         `json:"mode"`                   // 当前模式或无会话时的默认模式。
	Model                ModelData      `json:"model"`                  // 当前模式实际选择的模型。
	Phase                turn.Phase     `json:"phase"`                  // 当前轮次阶段，不混用 Runtime 阶段。
	PendingCount         int            `json:"pending_count"`          // 待处理输入数量。
	MessageCount         int            `json:"message_count"`          // 已持久化消息数量。
	LastUserContent      string         `json:"last_user_content"`      // 最近用户正文。
	LastAssistantContent string         `json:"last_assistant_content"` // 最近助手正文。
	Requests             []RequestData  `json:"requests"`               // 当前会话的运行请求。
	ToolUsage            map[string]int `json:"tool_usage"`             // 当前会话各工具的调用数量。
	TurnToolUsage        map[string]int `json:"turn_tool_usage"`        // 当前轮次的工具计数，独立于已保存统计。
	Context              ContextData    `json:"context"`                // 用量与上下文配置。
	Errors               []ItemError    `json:"errors"`                 // 可选统计读取失败，不伪造零值。
}
type StopData struct {
	SessionIDs    []string `json:"session_ids"`    // 停止所涉及的会话。
	RequestID     string   `json:"request_id"`     // 指定的请求；按会话停止时为空。
	AcceptedCount int      `json:"accepted_count"` // 实际接受取消的请求数量。
}
type ToolData struct {
	Name        string             `json:"name"`        // 工具或 Skill 名称。
	Description string             `json:"description"` // 完整说明。
	Source      string             `json:"source"`      // 注册来源。
	Risk        security.RiskLevel `json:"risk"`        // 声明风险。
	Tags        []string           `json:"tags"`        // 目录标签。
}
type ToolsData struct {
	Items []ToolData `json:"items"` // 可见目录。
	Name  string     `json:"name"`  // 单项操作目标，列表或重载时为空。
}
type RollbackData struct {
	ID       uint64    `json:"id"`        // 撤销记录标识。
	Path     string    `json:"path"`      // 用户操作的文件路径。
	Created  bool      `json:"created"`   // 原编辑是否创建文件。
	EditedAt time.Time `json:"edited_at"` // 原编辑时间。
	Deleted  bool      `json:"deleted"`   // 撤销是否删除新建文件。
	Revision string    `json:"revision"`  // 撤销后的版本。
}
type RollbacksData struct {
	Items []RollbackData `json:"items"` // 当前作用域的撤销记录。
}
type HelpItem struct {
	Name        string   `json:"name"`        // 规范命令名。
	Aliases     []string `json:"aliases"`     // 支持的别名。
	Args        string   `json:"args"`        // 参数说明。
	Description string   `json:"description"` // 简介。
	Help        string   `json:"help"`        // 完整帮助。
	Usage       string   `json:"usage"`       // 使用当前配置前缀构造的命令用法。
}
type HelpData struct {
	Items []HelpItem `json:"items"` // 已经过权限过滤的帮助项。
}
type HookData struct {
	ID          string `json:"id"`          // 插件或 Hook 标识。
	Name        string `json:"name"`        // Hook 名称。
	Description string `json:"description"` // 完整说明。
	Point       string `json:"point"`       // 注册点；仅运行态项为空。
	Priority    int    `json:"priority"`    // 注册优先级。
	Mode        string `json:"mode"`        // Worker 模式。
	Status      string `json:"status"`      // Worker 生命周期状态。
	Detail      string `json:"detail"`      // 完整详情。
	Active      int    `json:"active"`      // 在途调用数量。
	Waiting     int    `json:"waiting"`     // 等待租约数量。
}
type HooksData struct {
	Items   []HookData `json:"items"`   // 原始注册项与运行快照。
	Target  string     `json:"target"`  // 单项管理目标；列表或重载时为空。
	Notices []string   `json:"notices"` // 重载报告的独立提示。
}

type LogIdentityData struct {
	SessionID     string `json:"session_id"`      // 源会话，空字符串表示未声明。
	RunID         string `json:"run_id"`          // 跨请求的执行标识。
	Attempt       string `json:"attempt"`         // 当前执行尝试。
	RequestID     string `json:"request_id"`      // 源请求标识。
	RootRequestID string `json:"root_request_id"` // 根对话请求标识。
}
type LogData struct {
	Time         time.Time             `json:"time"`          // 日志接收时间。
	Category     events.LogCategory    `json:"category"`      // 日志类别。
	Level        string                `json:"level"`         // 严重程度。
	EventName    string                `json:"event_name"`    // 源事件名称。
	Module       string                `json:"module"`        // 源模块。
	Type         events.LogContentType `json:"type"`          // 内容类型。
	Content      json.RawMessage       `json:"content"`       // 原始 JSON 或 JSON 字符串，保留整数精度。
	Identity     LogIdentityData       `json:"identity"`      // 日志身份事实。
	ResultStatus events.ResultStatus   `json:"result_status"` // 源声明的执行结果。
	Raw          string                `json:"raw,omitempty"` // 仅原始查询附带的物理记录。
}
type LogsData struct {
	Items        []LogData `json:"items"`         // 匹配的有效日志。
	DamagedCount int       `json:"damaged_count"` // 扫描中遇到的损坏条数。
	DamagedLines []string  `json:"damaged_lines"` // 有界损坏样本。
}
type UsageBucket struct {
	Key              string `json:"key"`               // 分组标识；总计项为 total。
	Calls            int    `json:"calls"`             // 调用数量。
	PromptTokens     *int64 `json:"prompt_tokens"`     // 已知输入计数之和；nil 未知，-1 溢出。
	CompletionTokens *int64 `json:"completion_tokens"` // 已知输出计数之和。
	TotalTokens      *int64 `json:"total_tokens"`      // 已知总计数之和。
	CacheHitTokens   *int64 `json:"cache_hit_tokens"`  // 已知缓存命中计数之和。
	ElapsedMS        *int64 `json:"elapsed_ms"`        // 已知耗时之和。
}
type UsageData struct {
	GroupBy        string        `json:"group_by"`        // 原查询的聚合维度。
	Items          []UsageBucket `json:"items"`           // 排序后的聚合项。
	Total          UsageBucket   `json:"total"`           // 全部选中记录的总计。
	DamagedCount   int           `json:"damaged_count"`   // 不计入聚合的损坏记录数量。
	AvailableCalls int           `json:"available_calls"` // 本次查询可供区间选择的调用数。
}
type DoctorIssue struct {
	Path    string       `json:"path"`    // 涉及的配置路径。
	Level   config.Level `json:"level"`   // 原始诊断级别。
	Topic   string       `json:"topic"`   // 原始诊断主题。
	Message string       `json:"message"` // 完整问题说明。
	Field   []string     `json:"field"`   // 涉及的配置键。
	Missing bool         `json:"missing"` // 是否缺少声明。
}
type DoctorData struct {
	ConfigPath string        `json:"config_path"` // 当前主配置路径。
	Items      []DoctorIssue `json:"items"`       // 所有诊断发现，空数组表示没有发现。
}

type TokenUsage struct {
	PromptTokens     *int64 `json:"prompt_tokens"`     // 上游输入计数；nil 表示未知。
	CompletionTokens *int64 `json:"completion_tokens"` // 上游输出计数；不推算未知值。
	TotalTokens      *int64 `json:"total_tokens"`      // 上游总计数；不从输入输出相加。
	CacheHitTokens   *int64 `json:"cache_hit_tokens"`  // 上游缓存命中计数。
}
