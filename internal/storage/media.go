package storage

import (
	"context"
	"time"

	"elbot/internal/contextinfo"
)

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
	ChatType   contextinfo.ChatType // 会话类型，与原生 ID 共同标识会话。
	ChatID     string
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

type MediaOutput struct {
	Platform     string
	ChatType     contextinfo.ChatType // 会话类型，与原生 ID 共同标识会话。
	ChatID       string
	MessageID    string
	SegmentIndex int
	Kind         string
	MediaID      string
	OwnerID      string
	ExpiresAt    time.Time
}

type MediaRepository interface {
	SaveHistory(ctx context.Context, item HistoryMedia) error
	FindHistory(ctx context.Context, platform string, conversationKind contextinfo.ChatType, scopeID string, messageID string) ([]HistoryMedia, error)
	ListHistory(ctx context.Context, afterOwnerID string, limit int) ([]HistoryMedia, error)
	DeleteMediaHistoryAssociation(ctx context.Context, ownerID string) error
	GetMedia(ctx context.Context, id string) (*Media, error)
	UpsertMedia(ctx context.Context, media *Media) error
	GetSignedURL(ctx context.Context, mediaID string) (*MediaSignedURL, error)
	UpsertSignedURL(ctx context.Context, record *MediaSignedURL) error
	ClaimOrphans(ctx context.Context, cutoff time.Time) ([]Media, error)
	FinishDelete(ctx context.Context, id string) error
	Touch(ctx context.Context, id string, now time.Time) error
	SaveOutput(ctx context.Context, output MediaOutput) error
	FindOutputs(ctx context.Context, platform string, conversationKind contextinfo.ChatType, scopeID string, messageID string, now time.Time) ([]MediaOutput, error)
	ExpireOutputs(ctx context.Context, now time.Time) error
	CheckReferences(ctx context.Context) ([]string, error)
	RecoverInterrupted(ctx context.Context) error
}

type MediaReferenceRepository interface {
	AddMediaReference(ctx context.Context, reference *MediaReference) error
	AddMediaReferences(ctx context.Context, references []MediaReference) error
	RemoveMediaReference(ctx context.Context, reference MediaReference) error
	ListByOwner(ctx context.Context, ownerType, ownerID string) ([]MediaReference, error)
	ListMediaIDs(ctx context.Context, mediaID string) ([]MediaReference, error)
}
