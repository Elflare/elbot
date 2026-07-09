package storage

import (
	"context"
	"errors"
	"time"
)

var ErrNotFound = errors.New("not found")

const (
	SessionModeWork = "work"
	SessionModeChat = "chat"

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
	Metadata                 string
	CreatedAt                time.Time
}

type ContextSummary struct {
	ID             string
	SessionID      string
	FromMessageID  string
	ToMessageID    string
	Summary        string
	Provider       string
	Model          string
	SourceTokens   int
	SummaryTokens  int
	TotalTokens    int
	CacheHitTokens int
	TriggerReason  string
	Metadata       string
	CreatedAt      time.Time
}

type ToolCallRecord struct {
	ID            string
	SessionID     string
	ToolCallID    string
	ToolName      string
	ActorID       string
	RiskLevel     string
	Success       bool
	Error         string
	ResultPreview string
	StartedAt     time.Time
	FinishedAt    time.Time
	CreatedAt     time.Time
}

type CronJob struct {
	ID        string
	Name      string
	Handler   string
	Schedule  string
	Enabled   bool
	Metadata  string
	LastRunAt *time.Time
	NextRunAt *time.Time
	RunCount  int
	LastError string
	CreatedAt time.Time
	UpdatedAt time.Time
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
	ReplyToPlatformMessageID string
	Metadata                 string
	CreatedAt                time.Time
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
	LastRunAt time.Time
	NextRunAt *time.Time
	RunCount  int
	LastError string
	Enabled   bool
	UpdatedAt time.Time
}

type UpsertCronJobRequest struct {
	Name      string
	Handler   string
	Schedule  string
	Enabled   bool
	Metadata  string
	NextRunAt *time.Time
}

type CreateElnisEventRequest struct {
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
	ActorID                 string
	Platform                string
	PlatformScopeID         string
	IncludeAllPlatforms     bool
	IncludeSamePlatformCron bool
	IncludeArchived         bool
	ArchivedOnly            bool
	Query                   string
	Limit                   int
	Offset                  int
}

type SessionSummary struct {
	ID              string
	OwnerID         string
	Platform        string
	PlatformScopeID string
	Title           string
	Mode            string
	Status          string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	ArchivedAt      *time.Time
	PinnedAt        *time.Time
	MessageCount    int
	LastUserPreview string
	LastBotPreview  string
	MessagePreview  string
}

type Store interface {
	Sessions() SessionRepository
	Messages() MessageRepository
	ContextSummaries() ContextSummaryRepository
	ToolCalls() ToolCallRepository
	CronJobs() CronJobRepository
	ElnisEvents() ElnisEventRepository
	Close() error
}

type SessionRepository interface {
	Create(ctx context.Context, session *Session) error
	Get(ctx context.Context, id string) (*Session, error)
	Update(ctx context.Context, session *Session) error
	List(ctx context.Context, req ListSessionsRequest) ([]SessionSummary, error)
	Delete(ctx context.Context, id string) error
	DeleteExpired(ctx context.Context, cutoff time.Time) (int, error)
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

type ToolCallRepository interface {
	Create(ctx context.Context, record *ToolCallRecord) error
	UsageBySession(ctx context.Context, sessionID string) ([]ToolUsageSummary, error)
}

type CronJobRepository interface {
	Upsert(ctx context.Context, req UpsertCronJobRequest) (*CronJob, error)
	GetByName(ctx context.Context, name string) (*CronJob, error)
	List(ctx context.Context, includeDisabled bool) ([]CronJob, error)
	ListEnabled(ctx context.Context) ([]CronJob, error)
	UpdateNextRunAt(ctx context.Context, id string, nextRunAt *time.Time, updatedAt time.Time) error
	UpdateRunState(ctx context.Context, id string, state CronJobRunState) error
	DisableByName(ctx context.Context, name string) error
	DeleteByName(ctx context.Context, name string) error
}

type ElnisEventRepository interface {
	Create(ctx context.Context, req CreateElnisEventRequest) (*ElnisEvent, error)
	GetByKey(ctx context.Context, elwispName, source, sourceID string) (*ElnisEvent, error)
	Update(ctx context.Context, req UpdateElnisEventRequest) error
}

type ChatHistoryRepository interface {
	Append(ctx context.Context, message *ChatMessage) error
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
