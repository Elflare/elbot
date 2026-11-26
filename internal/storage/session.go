package storage

import (
	"context"
	"time"

	"elbot/internal/contextinfo"
)

const (
	SessionModeWork       = "work"
	SessionModeChat       = "chat"
	SessionModeBackground = "background"

	SessionStatusActive = "active"
)

type Session struct {
	TaskType          string // 后台任务类型，与 TaskID 组成任务身份。
	TaskID            string // 原生任务 ID，独立于聊天地址。
	TaskName          string // 任务展示名，不参与身份比较。
	ID                string
	ParentSessionID   string
	ForkFromMessageID string
	Owner             contextinfo.ActorIdentity
	Platform          string
	ChatType          contextinfo.ChatType // 会话类型，与原生 ID 共同标识会话。
	ChatID            string
	Mode              string
	Title             string
	Status            string
	Metadata          string
	CreatedAt         time.Time
	UpdatedAt         time.Time
	ArchivedAt        *time.Time
	PinnedAt          *time.Time
}

type ListSessionsRequest struct {
	Owner                         contextinfo.ActorIdentity
	Platform                      string
	ChatType                      contextinfo.ChatType // 会话类型，与原生 ID 共同标识会话。
	ChatID                        string
	IncludeAllPlatforms           bool
	IncludeSamePlatformBackground bool
	IncludeArchived               bool
	ArchivedOnly                  bool
	ExcludeSessionID              string
	Query                         string
	Limit                         int
	Offset                        int
	RecentFirst                   bool // 忽略置顶，按更新时间倒序查询最近会话。
	// MessageLimit bounds the ordered, unmodified message excerpts returned per session.
	MessageLimit int
}

type SessionSummary struct {
	ID                   string
	Owner                contextinfo.ActorIdentity
	Platform             string
	ChatType             contextinfo.ChatType // 会话类型，与原生 ID 共同标识会话。
	ChatID               string
	Title                string
	Mode                 string
	Status               string
	CreatedAt            time.Time
	UpdatedAt            time.Time
	ArchivedAt           *time.Time
	PinnedAt             *time.Time
	ParentSessionID      string // Parent session when this session was derived.
	ForkFromMessageID    string // Message that anchors this session branch.
	MessageCount         int
	LastUserContent      string
	LastAssistantContent string
	// Messages contains original role/content excerpts, with no display formatting.
	Messages []Message
}

type SessionRepository interface {
	// NextSequence 原子领取全局 Session 序号；独立于会话事务，已领取的号码不回收。
	NextSessionSequence(context.Context) (int64, error)
	CreateSession(ctx context.Context, session *Session) error
	CreateMaterial(context.Context, SessionMaterialCreate) error
	GetSession(ctx context.Context, id string) (*Session, error)
	MutateSession(ctx context.Context, id string, update func(*Session) error) (*Session, error)
	ListSessions(ctx context.Context, req ListSessionsRequest) ([]SessionSummary, error)
	CountSessions(ctx context.Context, req ListSessionsRequest) (int, error)
	DeleteSession(ctx context.Context, id string) error
	ListExpiredIDs(ctx context.Context, cutoff time.Time) ([]string, error)
	DeleteIfExpired(ctx context.Context, id string, cutoff time.Time) (bool, error)
}
