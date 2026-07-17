package session

import (
	"context"
	"time"

	"elbot/internal/storage"
)

type Scope struct {
	ActorID         string
	Platform        string
	PlatformScopeID string
	IsCLI           bool
}

type CreateRequest struct {
	Title    string
	Mode     string
	Metadata string
}

type ActivateModeRequest struct {
	Mode            string
	NewSessionTitle string
}

type ActivateModeResult struct {
	Session       *storage.Session
	AlreadyActive bool
}

type Status struct {
	Session           *storage.Session
	MessageCount      int
	LastUserPreview   string
	LastAnswerPreview string
}

type NamingConfig struct {
	TriggerStep int
}

type Config struct {
	NamingConfig
	DefaultMode string
}

type TitleResult struct {
	RawTitle string
}

type TitleGenerator interface {
	GenerateTitle(ctx context.Context, messages []storage.Message) (TitleResult, error)
}

type NamingScheduledEvent struct {
	SessionID    string
	TriggeredAt  time.Time
	MessageCount int
	TriggerStep  int
}

type NamingCompletedEvent struct {
	SessionID    string
	Title        string
	TriggeredAt  time.Time
	MessageCount int
}

type NamingFailedEvent struct {
	SessionID                string
	Title                    string
	Stage                    string
	LLMCall                  string
	GeneratedTitleRaw        string
	GeneratedTitleNormalized string
	InvalidReason            string
	Reason                   string
	Err                      error
	TriggeredAt              time.Time
	MessageCount             int
	FailureCount             int
	MaxFailures              int
	FallbackApplied          bool
	FallbackTitle            string
}

type NamingNotifier interface {
	NotifyNamingScheduled(ctx context.Context, event NamingScheduledEvent)
	NotifyNamingCompleted(ctx context.Context, event NamingCompletedEvent)
	NotifyNamingFailed(ctx context.Context, event NamingFailedEvent)
}
