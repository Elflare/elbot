package storage

import (
	"context"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/storage/maindb"
)

type (
	MediaSignedURL = maindb.MediaSignedURL
	Media          = maindb.Media
	HistoryMedia   = maindb.HistoryMedia
	MediaReference = maindb.MediaReference
	MediaOutput    = maindb.MediaOutput
)

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
