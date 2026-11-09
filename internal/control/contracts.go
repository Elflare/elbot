// Package control 提供与命令文本和 HTTP 无关的 ElBot 业务操作。
package control

import (
	"context"
	"encoding/json"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/logging"
	"elbot/internal/request"
	"elbot/internal/turn"
	"elbot/internal/workspace"
)

type Page struct {
	Page       int  `json:"page"`        // 从 1 开始。
	PageSize   int  `json:"page_size"`   // 每页容量。
	HasNext    bool `json:"has_next"`    // 是否还有更早/下一页记录。
	TotalPages int  `json:"total_pages"` // 空列表为 0。
}

type CommandInfo struct {
	Name        string           `json:"name"`
	Args        string           `json:"args"`
	Aliases     []string         `json:"aliases"`
	Description string           `json:"description"`
	Help        string           `json:"help"`
	MinRole     contextinfo.Role `json:"-"` // 由命令提供方声明，不接受客户端输入。
}
type ListCommandsRequest struct {
	Name string `json:"name"`
}
type ListCommandsResult struct {
	Items []CommandInfo `json:"items"`
}

type SessionData struct {
	ID                string     `json:"id"`
	Title             string     `json:"title"`
	Mode              string     `json:"mode"`
	Status            string     `json:"status"`
	Platform          string     `json:"platform"`
	ChatID            string     `json:"chat_id"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	ArchivedAt        *time.Time `json:"archived_at"`
	PinnedAt          *time.Time `json:"pinned_at"`
	ParentSessionID   string     `json:"parent_session_id"`
	ForkFromMessageID string     `json:"fork_from_message_id"`
}
type MessageData struct {
	ID                       string               `json:"id"`
	SessionID                string               `json:"session_id"`
	Role                     string               `json:"role"`
	Content                  string               `json:"content"`
	ParentMessageID          string               `json:"parent_message_id"`
	ReplyToPlatformMessageID string               `json:"reply_to_platform_message_id"`
	ReplyToMessageID         string               `json:"reply_to_message_id"`
	ToolCallID               string               `json:"tool_call_id"`
	Segments                 []llm.MessageSegment `json:"segments"` // 保留原有顺序与媒体引用；出口自行处理展示与授权。
	Metadata                 string               `json:"metadata"`
	ToolName                 string               `json:"tool_name,omitempty"`
	CreatedAt                time.Time            `json:"created_at"`
}
type SessionSummaryData struct {
	SessionData
	MessageCount         int           `json:"message_count"`
	Messages             []MessageData `json:"messages"`
	LastUserContent      string        `json:"last_user_content"`
	LastAssistantContent string        `json:"last_assistant_content"`
}
type ListSessionsRequest struct {
	Query          string `json:"query"`
	Page           int    `json:"page"`            // 从 1 开始，由调用方指定。
	PageSize       int    `json:"page_size"`       // 每页数量，由调用方指定。
	Archived       bool   `json:"archived"`        // true 仅归档；false 仅未归档。
	ExcludeCurrent bool   `json:"exclude_current"` // 排除当前绑定的会话。
}
type ListSessionsResult struct {
	Page
	Items            []SessionSummaryData `json:"items"`
	CurrentSessionID string               `json:"current_session_id"`
	Query            string               `json:"query"`
}
type GetSessionRequest struct {
	SessionID string `json:"session_id"`
}
type RenameSessionRequest struct {
	Target   string `json:"target"`    // 可选会话 ID 或标题；空值表示当前会话。
	Title    string `json:"title"`     // 重命名后的标题；Target 未匹配时与 Target 合并用于当前会话。
	PageSize int    `json:"page_size"` // 标题匹配的分页容量，由调用方指定。
}
type SetSessionArchivedRequest struct {
	SessionID string `json:"session_id"`
	Archived  bool   `json:"archived"`
	Confirmed bool   `json:"confirmed"`
}
type SetSessionPinnedRequest struct {
	SessionID string `json:"session_id"`
	Pinned    bool   `json:"pinned"`
}
type DeleteSessionRequest struct {
	SessionID string `json:"session_id"`
	Confirmed bool   `json:"confirmed"`
}
type CleanSessionsRequest struct {
	Confirmed bool      `json:"confirmed"`
	Before    time.Time `json:"before"` // 清理截止时间，由调用方根据保留期或用户选择计算。
}
type ResumeSessionRequest struct {
	SessionID    string `json:"session_id"`
	MessageLimit int    `json:"message_limit"` // 返回最近消息的数量；0 不查询。
}
type ForkSessionRequest struct {
	SessionID    string `json:"session_id"`
	MessageID    string `json:"message_id"`
	MessageLimit int    `json:"message_limit"` // 返回最近消息的数量；0 不查询。
}
type CompactSessionRequest struct {
	SessionID string `json:"session_id"`
}
type SetModeRequest struct {
	Mode            string `json:"mode"`
	Text            string `json:"text"`
	NewSessionTitle string `json:"new_session_title"` // 需要新建会话时使用的标题。
}

// Continuation 必须在业务结果成功交付后进入 Agent 输入链；不序列化成客户端可提交的指令。
type Continuation struct {
	Kind      string // input 为普通输入，append 为当前追加等待的续跑。
	SessionID string // 已完成模式切换的目标会话。
	Text      string // 保留原始输入，不 TrimSpace。
}
type SessionChangeResult struct {
	Messages         []MessageData `json:"messages"` // 按调用方选择读取的完整原始消息。
	Session          *SessionData  `json:"session"`
	SessionID        string        `json:"session_id"`
	CurrentSessionID string        `json:"current_session_id"`
	AffectedCount    int           `json:"affected_count"`
	Unchanged        bool          `json:"unchanged"`
	Continuation     *Continuation `json:"-"`
}
type NewSessionResult = SessionChangeResult
type GetSessionResult = SessionData
type RenameSessionResult = SessionChangeResult
type SetSessionArchivedResult = SessionChangeResult
type SetSessionPinnedResult = SessionChangeResult
type DeleteSessionResult = SessionChangeResult
type CleanSessionsResult = SessionChangeResult
type ResumeSessionResult = SessionChangeResult
type ForkSessionResult = SessionChangeResult
type CompactSessionResult = SessionChangeResult
type SetModeResult = SessionChangeResult

type SessionMessagesRequest struct {
	SessionID string `json:"session_id"`
	Page      int    `json:"page"`
	PageSize  int    `json:"page_size"`
}
type SessionMessagesResult struct {
	Page
	SessionID string        `json:"session_id"`
	Items     []MessageData `json:"items"`
}
type WorkspaceResult struct {
	SessionID   string `json:"session_id"`
	AgentPath   string `json:"agent_path"`
	UserPath    string `json:"user_path"`
	AgentToUser bool   `json:"agent_to_user"`
	UserToAgent bool   `json:"user_to_agent"`
}
type WorkspaceDirectoriesRequest struct {
	Path string
}
type WorkspaceDirectoriesResult = workspace.DirectoryListing
type WorkspaceFilesRequest struct {
	// Side selects the user or Agent workspace.
	Side string
	// Path is relative to the selected workspace root; empty selects the root.
	Path string
}
type WorkspaceFilesResult = workspace.WorkspaceFileListing
type SetUserWorkspaceRequest struct {
	Path string `json:"path"`
}
type SetWorkspaceSyncRequest struct {
	AgentToUser bool `json:"agent_to_user"`
	UserToAgent bool `json:"user_to_agent"`
}
type ToolDetailRequest struct {
	Name string // 工具目录名称。
}
type WorkspaceGitRequest struct {
	Side string
	Path string
}
type WorkspaceGitResult = workspace.GitStatus

type StatusResult struct {
	Session              *SessionData   `json:"session"`
	Mode                 string         `json:"mode"`
	Model                ModelData      `json:"model"`
	Phase                turn.Phase     `json:"phase"`
	PendingCount         int            `json:"pending_count"`
	MessageCount         int            `json:"message_count"`
	LastUserContent      string         `json:"last_user_content"`
	LastAssistantContent string         `json:"last_assistant_content"`
	Requests             []RequestData  `json:"requests"`
	Context              ContextData    `json:"context"`
	ToolUsage            map[string]int `json:"tool_usage"` // 只读统计，未知时为 nil。
	TurnToolUsage        map[string]int `json:"turn_tool_usage"`
	Errors               []ItemError    `json:"errors"` // 非必要统计读取失败，不伪造零值。
	DefaultMode          string         `json:"default_mode"`
	CompactModel         ModelData      `json:"compact_model"`
	Platform             string         `json:"platform"`
	ChatID               string         `json:"chat_id"`
}
type RequestData struct {
	ID           string       `json:"id"`
	ParentID     string       `json:"parent_id"`
	SessionID    string       `json:"session_id"`
	SessionTitle string       `json:"session_title"`
	Kind         request.Kind `json:"kind"`
	Label        string       `json:"label"`
	StartedAt    time.Time    `json:"started_at"`
	Stopping     bool         `json:"stopping"`
}
type ListRequestsResult struct {
	Items []RequestData `json:"items"`
}
type StopRequestRequest struct {
	RequestID string `json:"request_id"`
}
type StopResult struct {
	SessionIDs    []string `json:"session_ids"`
	RequestID     string   `json:"request_id"`
	AcceptedCount int      `json:"accepted_count"` // 接受取消的数量，不表示请求已退出。
	State         string   `json:"state"`          // stopping 或 stopped。
}
type StopRequestResult = StopResult
type StopSessionResult = StopResult
type StopAllRequestsResult = StopResult

type ModelData struct {
	Provider string   `json:"provider"`
	Model    string   `json:"model"`
	Index    int      `json:"index"`
	Modes    []string `json:"modes"`
	Compact  bool     `json:"compact"`
	Naming   bool     `json:"naming"`
}
type ListModelsRequest struct {
	Query string `json:"query"`
}
type ItemError struct {
	ID    string `json:"id"`
	Error *Error `json:"error"`
}
type ListModelsResult struct {
	Query  string      `json:"query"`
	Items  []ModelData `json:"items"`
	Errors []ItemError `json:"errors"`
}
type RefreshModelsResult = ListModelsResult
type SelectModelRequest struct {
	Target   string `json:"target"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
}
type SelectModelResult struct {
	Target string    `json:"target"`
	Model  ModelData `json:"model"`
}

type CurrentConfirmationRequest struct {
	Refresh bool `json:"refresh"` // 读取详情时刷新原等待的超时，普通查询不刷新。
}
type CurrentConfirmationResult struct {
	SessionID string `json:"session_id"`
	Kind      string `json:"kind"` // tool 或 append。
	Detail    string `json:"detail"`
	ToolName  string `json:"tool_name"`
	Arguments string `json:"arguments"`
	Risk      string `json:"risk"`
}
type RespondConfirmationRequest struct {
	Decision string `json:"decision"` // approve 或 reject。
	Scope    string `json:"scope"`    // current、tool 或 all。
	Extra    string `json:"extra"`    // 批准附加输入，或拒绝原因。
}
type RespondConfirmationResult struct {
	Accepted     bool          `json:"accepted"`
	Continuation *Continuation `json:"-"`
}
type HookInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Point       string `json:"point"`
	Priority    int    `json:"priority"`
	Mode        string `json:"mode"`
	Status      string `json:"status"`
	Detail      string `json:"detail"`
	Active      int    `json:"active"`
	Waiting     int    `json:"waiting"`
}
type ListHooksResult struct {
	Items []HookInfo `json:"items"`
}
type ReloadHooksResult struct {
	Notices []string `json:"notices"` // 提供方重载产生的警告，失败时也保留。
}
type GetHookRequest struct {
	HookID string `json:"hook_id"`
}
type GetHookResult struct {
	Items []HookInfo `json:"items"`
} // 一个插件可拥有多个注册项。
type StartHookRequest = GetHookRequest
type StopHookRequest = GetHookRequest
type RestartHookRequest = GetHookRequest

type QueryLogsRequest struct {
	Category string           `json:"category"` // log、audit 或 elnis。
	Query    logging.LogQuery `json:"-"`        // 现有类型化筛选；来源由 Category 决定，忽略 Query.Prefix。
}
type LogData struct {
	Time         time.Time             `json:"time"`
	Category     events.LogCategory    `json:"category"`
	Level        string                `json:"level"`
	EventName    string                `json:"event_name"`
	Module       string                `json:"module"`
	Type         events.LogContentType `json:"type"`
	Content      json.RawMessage       `json:"content"` // JSON 内容不经过 float64，保留整数精度。
	Identity     LogIdentityData       `json:"identity"`
	ResultStatus events.ResultStatus   `json:"result_status"`
	Raw          string                `json:"raw,omitempty"`
}
type QueryLogsResult struct {
	Items        []LogData `json:"items"`
	DamagedCount int       `json:"damaged_count"`
	DamagedLines []string  `json:"damaged_lines"`
}
type LogIdentityData struct {
	SessionID     string `json:"session_id"`
	RunID         string `json:"run_id"`
	Attempt       string `json:"attempt"`
	RequestID     string `json:"request_id"`
	RootRequestID string `json:"root_request_id"`
}
type QueryUsageRequest struct {
	Query   logging.LogQuery `json:"-"`        // 复用日志的时间与内容筛选，固定读取 llm_usage 审计事件。
	GroupBy string           `json:"group_by"` // model、day 或 session，由调用方指定。
	First   int              `json:"first"`    // 按新到旧、从 1 开始的调用区间。
	Last    int              `json:"last"`     // 包含尾项；查询只读取到该位置，无固定上限。
}
type UsageBucket struct {
	Key              string `json:"key"`
	Calls            int    `json:"calls"`
	PromptTokens     *int64 `json:"prompt_tokens"` // 任一选中记录未知、非法或溢出时为 nil。
	CompletionTokens *int64 `json:"completion_tokens"`
	TotalTokens      *int64 `json:"total_tokens"`
	CacheHitTokens   *int64 `json:"cache_hit_tokens"`
	ElapsedMS        *int64 `json:"elapsed_ms"`
}
type QueryUsageResult struct {
	GroupBy        string        `json:"group_by"`
	Items          []UsageBucket `json:"items"`
	Total          UsageBucket   `json:"total"`
	DamagedCount   int           `json:"damaged_count"`
	AvailableCalls int           `json:"available_calls"`
	RangeTruncated bool          `json:"range_truncated"`
}

type ListFilesRequest struct {
	Root string `json:"root"`
	Path string `json:"path"`
}
type FileEntry struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Kind     string `json:"kind"`
	Size     int64  `json:"size"`
	Exists   bool   `json:"exists"`   // 固定配置项可尚未创建。
	Writable bool   `json:"writable"` // 由根策略决定，日志始终为 false。
}
type ListFilesResult struct {
	Root  string      `json:"root"`
	Path  string      `json:"path"`
	Items []FileEntry `json:"items"`
}
type ReadFileRequest struct {
	Root   string `json:"root"`
	Path   string `json:"path"`
	Offset int64  `json:"offset"` // 原始字节偏移，调用方决定读取范围。
	Limit  int64  `json:"limit"`  // 本次读取的字节数；0 读到文件末尾，无默认大小或上限。
}
type ReadFileResult struct {
	Root       string `json:"root"`
	Path       string `json:"path"`
	Content    string `json:"content"`  // 原始字节片段，文本解码由调用方负责。
	Revision   string `json:"revision"` // 本次读取内容的 revision；覆盖保存应使用全文读取的 revision。
	Size       int64  `json:"size"`
	Offset     int64  `json:"offset"`
	NextOffset int64  `json:"next_offset"`
	Truncated  bool   `json:"truncated"`
}
type WriteFileRequest struct {
	Root             string `json:"root"`
	Path             string `json:"path"`
	Content          string `json:"content"`
	ExpectedRevision string `json:"expected_revision"` // 已有文件必填；创建时必须为空。
	Create           bool   `json:"create"`            // 仅允许目标不存在，不表示允许覆盖。
}
type WriteFileResult struct {
	Root     string `json:"root"`
	Path     string `json:"path"`
	Revision string `json:"revision"`
	Created  bool   `json:"created"`
}

// RequestRestartResult 的动作不下发客户端；调用方在交付后执行 Commit，交付失败则 Cancel。
type RequestRestartResult struct {
	Accepted bool                        `json:"accepted"`
	Commit   func(context.Context) error `json:"-"`
	Cancel   func()                      `json:"-"`
}

type RemoveSkillRequest struct {
	Name      string `json:"name"`      // 技能名称。
	Confirmed bool   `json:"confirmed"` // 调用方已获得删除确认。
}
type RollbackByIDRequest struct {
	ID uint64 `json:"id"` // 原始文件编辑记录 ID。
}
