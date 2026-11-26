package storage

import (
	"context"
	"time"

	"elbot/internal/storage/maindb"
)

type (
	Session             = maindb.Session
	ListSessionsRequest = maindb.ListSessionsRequest
	SessionSummary      = maindb.SessionSummary
)

const (
	SessionModeWork       = maindb.SessionModeWork
	SessionModeChat       = maindb.SessionModeChat
	SessionModeBackground = maindb.SessionModeBackground
	SessionStatusActive   = maindb.SessionStatusActive
)

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
