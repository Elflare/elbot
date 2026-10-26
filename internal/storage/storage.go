package storage

import (
	"context"
	"elbot/internal/events"
	"errors"
	"time"
)

var ErrNotFound = errors.New("not found")

const (
	SessionModeWork       = "work"
	SessionModeChat       = "chat"
	SessionModeBackground = "background"

	SessionStatusActive = "active"
	SessionStatusPaused = "paused"
	SessionStatusClosed = "closed"

	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

type Session struct {
	ID                string
	ParentSessionID   string
	ForkFromMessageID string
	OwnerID           string
	Platform          string
	PlatformScopeID   string
	Mode              string
	Title             string
	Status            string
	Metadata          string
	CreatedAt         time.Time
	UpdatedAt         time.Time
	ArchivedAt        *time.Time
	PinnedAt          *time.Time
}

type Message struct {
	ID                       string
	SessionID                string
	Role                     string
	Content                  string
	ParentMessageID          string
	ReplyToPlatformMessageID string
	ReplyToMessageID         string
	ToolCallID               string
	Segments                 string
	Metadata                 string
	CreatedAt                time.Time
}

type ContextSummary struct {
	ID            string
	SessionID     string
	FromMessageID string
	ToMessageID   string
	Summary       string
	Provider      string
	Model         string
	// SourceTokens is supplied by the compactor; nil persists as SQL NULL (unknown).
	SourceTokens *int64
	// SummaryTokens is the reported output count; non-nil zero is an explicit zero.
	SummaryTokens *int64
	// TotalTokens is the reported total; nil is unknown and is never inferred.
	TotalTokens *int64
	// CacheHitTokens is the reported cache count; nil persists as SQL NULL.
	CacheHitTokens *int64
	TriggerReason  string
	Metadata       string
	CreatedAt      time.Time
}

type ToolCallRecord struct {
	ID         string
	SessionID  string
	ToolCallID string
	ToolName   string
	ActorID    string
	RiskLevel  string
	Success    bool
	Error      string
	StartedAt  time.Time
	FinishedAt time.Time
	CreatedAt  time.Time
}

type CronJob struct {
	ID            string
	Name          string
	Handler       string
	Schedule      string
	Enabled       bool
	Metadata      string
	DeliveryState string
	DeliveryToken string
	LastRunAt     *time.Time
	NextRunAt     *time.Time
	RunCount      int
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// CronMetadata 是用户 Cron 任务的持久化定义，供管理工具与后台执行共同读取。
// Storage 只声明数据格式；任务校验、调度及投递策略由调用方负责。
type CronMetadata struct {
	Kind      string          `json:"kind"`          // 持久化定义的种类。
	Version   int             `json:"version"`       // 定义格式版本。
	Title     string          `json:"title"`         // 展示标题。
	CreatedBy CronActor       `json:"created_by"`    // 创建者身份快照。
	Schedule  CronSchedule    `json:"schedule"`      // 一次性或周期计划。
	Trigger   CronTrigger     `json:"trigger"`       // 直接通知或 LLM 任务。
	Target    CronTarget      `json:"target"`        // 报告投递范围。
	LLM       CronLLMMetadata `json:"llm,omitempty"` // LLM 模型和工具选择。
}

const (
	CronHandlerName  = "builtin.cron"
	CronMetadataKind = "llm_cron"
	CronTimeLayout   = "2006-01-02 15:04:05"
)

type CronScheduleMode string

const (
	CronScheduleOnce CronScheduleMode = "once"
	CronScheduleCron CronScheduleMode = "cron"
)

type CronTriggerMode string

const (
	CronTriggerDirect CronTriggerMode = "direct"
	CronTriggerLLM    CronTriggerMode = "llm"
)

type CronActor struct {
	ActorID        string `json:"actor_id"`               // 项目内身份标识。
	Platform       string `json:"platform"`               // 创建者所属平台。
	PlatformUserID string `json:"platform_user_id"`       // 平台用户标识。
	DisplayName    string `json:"display_name,omitempty"` // 创建时的展示名称。
}

type CronSchedule struct {
	Mode     CronScheduleMode `json:"mode"`                // 计划模式。
	RunAt    string           `json:"run_at,omitempty"`    // 本地时间，格式为 CronTimeLayout。
	CronExpr string           `json:"cron_expr,omitempty"` // 五字段 Cron 表达式。
}

type CronTrigger struct {
	Mode    CronTriggerMode `json:"mode"`    // 执行方式。
	Message string          `json:"message"` // 通知正文或 ELyph 任务文本。
}

type CronTarget struct {
	AllEnabledPlatforms bool   `json:"all_enabled_platforms"` // 是否向全部启用平台投递。
	SourcePlatform      string `json:"source_platform"`       // 非广播时的来源平台。
}

type CronLLMMetadata struct {
	ToolListNames []string `json:"tool_list_names,omitempty"` // 工具、Skill 或标签选择。
	ModelProvider string   `json:"model_provider,omitempty"`  // 可选的模型提供方。
	Model         string   `json:"model,omitempty"`           // 与提供方配对的模型名称。
}

// CronTaskStatus 是 DeliveryState 中供管理工具读取的状态投影。
// 后台保存投递进度时同时写入；查询方不重新执行投递完成判定。
type CronTaskStatus struct {
	DeliveryComplete bool `json:"delivery_complete,omitempty"` // 后台已判定本轮报告投递完成或被前台接管。
}

type ElnisEvent struct {
	ID               string
	EventKey         string
	TokenName        string
	ElwispName       string
	Source           string
	SourceID         string
	Tags             string
	Mode             string
	ModelSlot        string
	ContentHash      string
	ToolDeclarations string
	ToolHash         string
	RequestedTargets string
	ResolvedTargets  string
	Status           string
	SessionID        string
	Result           string
	Error            string
	ReceivedAt       time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type ElnisReportDelivery struct {
	ID        string
	EventID   string
	Ordinal   int
	Target    string
	Output    string
	MessageID string
	Status    string
	Receipt   string
	Error     string
	Attempts  int
	CreatedAt time.Time
	UpdatedAt time.Time
}

const (
	ElnisReportDeliveryPending   = "pending"
	ElnisReportDeliveryFailed    = "failed"
	ElnisReportDeliveryDelivered = "delivered"
)

// PlatformMessageAlias associates a platform reference index with its message ID.
type PlatformMessageAlias struct {
	Platform          string    // Platform owning the reference index.
	PlatformScopeID   string    // Conversation in which the index is valid.
	Alias             string    // Platform reference index, not a message ID.
	PlatformMessageID string    // Actual message ID used by history and output mappings.
	CreatedAt         time.Time // Registration time used by chat history retention.
}

type ChatMessage struct {
	Seq                      int64
	ID                       string
	Platform                 string
	PlatformScopeID          string
	ScopeType                string
	PlatformMessageID        string
	SenderID                 string
	SenderName               string
	Text                     string
	Raw                      string
	Segments                 string
	ReplyToPlatformMessageID string
	Metadata                 string
	CreatedAt                time.Time
}

// MediaSignedURL caches a download URL independently of the media's lifetime.
type MediaSignedURL struct {
	MediaID string
	URL     string
	// ExpiresAt is the URL's absolute expiry, not the media retention deadline.
	ExpiresAt time.Time
}

type Media struct {
	Deleting       bool
	ID             string
	Name           string
	MIMEType       string
	Size           int64
	LocalPath      string
	Backend        string
	ObjectKey      string
	SourcePlatform string
	SourceURL      string
	SourceFileID   string
	CreatedAt      time.Time
	LastAccessedAt time.Time
	ExpiresAt      *time.Time
}

// HistoryMedia associates one ordered media position with an observed chat message.
type HistoryMedia struct {
	HistoryID  string
	Platform   string
	ScopeID    string
	MessageID  string
	MediaIndex int
	Kind       string
	MediaID    string
	OwnerID    string
}
type MediaReference struct {
	MediaID   string
	OwnerType string
	OwnerID   string
	Purpose   string
	SessionID string
	CreatedAt time.Time
}

type ChatHistorySearchRequest struct {
	Platform        string
	PlatformScopeID string
	QueryTerms      []string
	QueryMode       string
	SenderID        string
	SenderNameQuery string
	Since           *time.Time
	Until           *time.Time
	Limit           int
}

type ChatHistoryAroundRequest struct {
	Platform          string
	PlatformScopeID   string
	PlatformMessageID string
	Before            int
	After             int
}

type CronJobRunState struct {
	LastRunAt time.Time // 本次执行开始时间，执行结束后记录。
	UpdatedAt time.Time // 本次运行事实的记录时间；次数由仓库原子递增。
}

type UpsertCronJobRequest struct {
	Name          string
	Handler       string
	Schedule      string
	Enabled       bool
	Metadata      string
	NextRunAt     *time.Time
	ResetDelivery bool
}

type CreateElnisEventRequest struct {
	MediaIDs         []string
	EventKey         string
	TokenName        string
	ElwispName       string
	Source           string
	SourceID         string
	Tags             string
	Mode             string
	ModelSlot        string
	ContentHash      string
	ToolDeclarations string
	ToolHash         string
	RequestedTargets string
	ResolvedTargets  string
	Status           string
	Result           string
	Error            string
	ReceivedAt       time.Time
	CreatedAt        time.Time
}

type UpdateElnisEventRequest struct {
	ID              string
	ResolvedTargets string
	Status          string
	SessionID       string
	Result          string
	Error           string
}

type CreateElnisReportDeliveryRequest struct {
	Target    string
	Output    string
	MessageID string
}

type PrepareElnisReportRequest struct {
	EventID           string
	ResolvedTargets   string
	SessionID         string
	Result            string
	Deliveries        []CreateElnisReportDeliveryRequest
	ResultReadyStatus string
}

type ToolUsageSummary struct {
	ToolName string
	Count    int
}

type PlatformMessageMap struct {
	ID                string
	Platform          string
	PlatformScopeID   string
	PlatformMessageID string
	MessageID         string
	SessionID         string
	CreatedAt         time.Time
}

type ListSessionsRequest struct {
	ActorID                       string
	Platform                      string
	PlatformScopeID               string
	IncludeAllPlatforms           bool
	IncludeSamePlatformBackground bool
	IncludeArchived               bool
	ArchivedOnly                  bool
	ExcludeSessionID              string
	OrderByUpdatedAt              bool
	Query                         string
	Limit                         int
	Offset                        int
	// MessageLimit bounds the ordered, unmodified message excerpts returned per session.
	MessageLimit int
}

type SessionSummary struct {
	ID                   string
	OwnerID              string
	Platform             string
	PlatformScopeID      string
	Title                string
	Mode                 string
	Status               string
	CreatedAt            time.Time
	UpdatedAt            time.Time
	ArchivedAt           *time.Time
	PinnedAt             *time.Time
	MessageCount         int
	LastUserContent      string
	LastAssistantContent string
	// Messages contains original role/content excerpts, with no display formatting.
	Messages []Message
}

type Store interface {
	Sessions() SessionRepository
	Messages() MessageRepository
	Dialogues() DialogueRepository
	Media() MediaRepository
	MediaReferences() MediaReferenceRepository
	ContextSummaries() ContextSummaryRepository
	ToolCalls() ToolCallRepository
	CronJobs() CronJobRepository
	ElnisEvents() ElnisEventRepository
	Close() error
}

type SessionRepository interface {
	Create(ctx context.Context, session *Session) error
	CreateMaterial(context.Context, SessionMaterialCreate) (events.LogResult, error)
	Get(ctx context.Context, id string) (*Session, error)
	Mutate(ctx context.Context, id string, update func(*Session) error) (*Session, error)
	List(ctx context.Context, req ListSessionsRequest) ([]SessionSummary, error)
	Delete(ctx context.Context, id string) error
	ListExpiredIDs(ctx context.Context, cutoff time.Time) ([]string, error)
	DeleteIfExpired(ctx context.Context, id string, cutoff time.Time) (bool, error)
}

type MessageRepository interface {
	Append(ctx context.Context, message *Message) error
	Get(ctx context.Context, id string) (*Message, error)
	ListBySession(ctx context.Context, sessionID string) ([]Message, error)
	ListBySessionUpTo(ctx context.Context, sessionID, toMessageID string) ([]Message, error)
	ListBySessionAfter(ctx context.Context, sessionID, afterMessageID string) ([]Message, error)
	ListBySessionAfterUpTo(ctx context.Context, sessionID, afterMessageID, toMessageID string) ([]Message, error)
	MapPlatformMessage(ctx context.Context, mapping PlatformMessageMap) error
	FindByPlatformMessage(ctx context.Context, platform, scopeID, platformMessageID string) (*Message, error)
}

type MediaOutput struct {
	Platform     string
	ScopeID      string
	MessageID    string
	SegmentIndex int
	Kind         string
	MediaID      string
	OwnerID      string
	ExpiresAt    time.Time
}

type MediaRepository interface {
	SaveHistory(ctx context.Context, item HistoryMedia) error
	FindHistory(ctx context.Context, platform, scopeID, messageID string) ([]HistoryMedia, error)
	ListHistory(ctx context.Context, afterOwnerID string, limit int) ([]HistoryMedia, error)
	DeleteHistory(ctx context.Context, ownerID string) error
	Get(ctx context.Context, id string) (*Media, error)
	Upsert(ctx context.Context, media *Media) error
	GetSignedURL(ctx context.Context, mediaID string) (*MediaSignedURL, error)
	UpsertSignedURL(ctx context.Context, record *MediaSignedURL) error
	DeleteOrphans(ctx context.Context, cutoff time.Time) ([]Media, error)
	ClaimOrphans(ctx context.Context, cutoff time.Time) ([]Media, error)
	FinishDelete(ctx context.Context, id string) error
	Touch(ctx context.Context, id string, now time.Time) error
	SaveOutput(ctx context.Context, output MediaOutput) error
	FindOutputs(ctx context.Context, platform, scopeID, messageID string, now time.Time) ([]MediaOutput, error)
	ExpireOutputs(ctx context.Context, now time.Time) error
	CheckReferences(ctx context.Context) ([]string, error)
	RecoverInterrupted(ctx context.Context) error
}

type MediaReferenceRepository interface {
	Add(ctx context.Context, reference *MediaReference) error
	AddAll(ctx context.Context, references []MediaReference) error
	Remove(ctx context.Context, reference MediaReference) error
	ListByOwner(ctx context.Context, ownerType, ownerID string) ([]MediaReference, error)
	ListMediaIDs(ctx context.Context, mediaID string) ([]MediaReference, error)
}

type ToolCallRepository interface {
	Create(ctx context.Context, record *ToolCallRecord) error
	SuccessfulIDs(ctx context.Context, toolCallIDs []string) (map[string]bool, error)
	UsageBySession(ctx context.Context, sessionID string) ([]ToolUsageSummary, error)
}

type CronJobRepository interface {
	Upsert(ctx context.Context, req UpsertCronJobRequest) (*CronJob, error)
	GetByName(ctx context.Context, name string) (*CronJob, error)
	List(ctx context.Context, includeDisabled bool) ([]CronJob, error)
	ListEnabled(ctx context.Context) ([]CronJob, error)
	UpdateNextRunAt(ctx context.Context, id string, nextRunAt *time.Time, updatedAt time.Time) error
	UpdateRunState(ctx context.Context, id string, state CronJobRunState) error
	// CompareAndSwapDelivery saves actual sent-message mappings atomically with the
	// conditional progress update. A stale token preserves mappings but changes no progress.
	CompareAndSwapDelivery(ctx context.Context, id, expectedToken, nextToken, deliveryState string, mappings []PlatformMessageMap) (bool, error)
	DisableByName(ctx context.Context, name string) error
	DisableByNameIfDeliveryToken(ctx context.Context, name, deliveryToken string) (bool, error)
	DeleteByName(ctx context.Context, name string) error
}

type ElnisEventRepository interface {
	// FailInterrupted applies the caller's recovery policy and releases event references atomically.
	FailInterrupted(ctx context.Context, fromStatuses []string, failedStatus, reason string) error
	Create(ctx context.Context, req CreateElnisEventRequest) (*ElnisEvent, error)
	Get(ctx context.Context, id string) (*ElnisEvent, error)
	GetByKey(ctx context.Context, elwispName, source, sourceID string) (*ElnisEvent, error)
	Update(ctx context.Context, req UpdateElnisEventRequest) error
	PrepareReport(ctx context.Context, req PrepareElnisReportRequest) error
	ResetDeliveringReports(ctx context.Context, deliveringStatus, resultReadyStatus string) error
	ListResultReadyReportIDs(ctx context.Context, resultReadyStatus string) ([]string, error)
	ClaimReport(ctx context.Context, eventID, resultReadyStatus, deliveringStatus string) (bool, error)
	ReleaseReport(ctx context.Context, eventID, deliveringStatus, resultReadyStatus, deliveryError string) error
	ListReportDeliveries(ctx context.Context, eventID string) ([]ElnisReportDelivery, error)
	StartReportDelivery(ctx context.Context, id string) error
	// MarkReportDeliveryFailed saves partial receipt mappings and retry state in one transaction.
	MarkReportDeliveryFailed(ctx context.Context, eventID, deliveryID, resultReadyStatus, deliveryError, receipt string, mappings []PlatformMessageMap) error
	// MarkReportDeliveriesDelivered commits one sent batch, its receipt and mappings together.
	MarkReportDeliveriesDelivered(ctx context.Context, deliveryIDs []string, receipt string, mappings []PlatformMessageMap) error
	CompleteReport(ctx context.Context, eventID, deliveringStatus, completedStatus string) error
}

type ChatHistoryRepository interface {
	Append(ctx context.Context, message *ChatMessage) error
	MapPlatformMessageAlias(ctx context.Context, mapping PlatformMessageAlias) error
	ResolvePlatformMessageAlias(ctx context.Context, platform, scopeID, alias string) (string, error)
	GetByPlatformMessage(ctx context.Context, platform, scopeID, platformMessageID string) (*ChatMessage, error)
	Search(ctx context.Context, req ChatHistorySearchRequest) ([]ChatMessage, error)
	Around(ctx context.Context, req ChatHistoryAroundRequest) ([]ChatMessage, error)
	DeleteBefore(ctx context.Context, cutoff time.Time) (int, error)
}

type ContextSummaryRepository interface {
	Create(ctx context.Context, summary *ContextSummary) error
	LatestBySession(ctx context.Context, sessionID string) (*ContextSummary, error)
	LatestBySessionUpTo(ctx context.Context, sessionID, toMessageID string) (*ContextSummary, error)
}
