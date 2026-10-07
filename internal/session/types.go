package session

import (
	"context"
	"elbot/internal/contextinfo"
	"time"

	"elbot/internal/storage"
)

type Scope struct {
	ConversationKind contextinfo.ConversationKind
	ActorID          string
	Platform         string
	PlatformScopeID  string
	IsCLI            bool
}

// Key identifies the actor/platform/scope that owns a current session.
func (s Scope) Key() string {
	return s.ActorID + "\x00" + s.Platform + "\x00" + s.PlatformScopeID
}

type CreateRequest struct {
	// ID may be preallocated for an execution handoff holding both session gates.
	ID       string
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
	Provider string
	Model    string
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
	Provider     string
	Model        string
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
	FallbackErr              error
	TriggeredAt              time.Time
	MessageCount             int
	FailureCount             int
	MaxFailures              int
	FallbackApplied          bool
	FallbackTitle            string
	Provider                 string
	Model                    string
}
