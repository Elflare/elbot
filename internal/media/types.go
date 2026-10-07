package media

import (
	"context"
	"io"
	"sync"
	"time"

	"elbot/internal/config"
	"elbot/internal/storage"
)

const IDPrefix = "media:"

type Source struct {
	Platform string
	URL      string
	FileID   string
}

// ImportLimits overrides import settings for one materialization call.
// Nonpositive values retain the manager defaults; MaxImportBytes can only
// tighten the manager limit.
type ImportLimits struct {
	MaxImportBytes  int64
	DownloadTimeout time.Duration
}

type Input struct {
	Name     string
	MIMEType string
	Source   Source
}

type Backend interface {
	Put(ctx context.Context, id string, input io.Reader, size int64, contentType string) (location string, err error)
	Open(ctx context.Context, media *storage.Media) (io.ReadCloser, error)
	Remove(ctx context.Context, media *storage.Media) error
	PresignGet(ctx context.Context, media *storage.Media, expiry time.Duration) (string, error)
}

type lazyBackend struct {
	manager *Manager
}

type Manager struct {
	objects         objectLocks
	cleanupMu       objectLocks
	local           Backend
	Store           storage.Store
	History         storage.ChatHistoryRepository
	Backend         Backend
	Remote          Backend
	remoteFactory   func(context.Context) (Backend, error)
	remoteMu        sync.Mutex
	MaxImportBytes  int64
	DownloadTimeout time.Duration
	Root            string
	FileDelivery    config.FileDeliveryConfig
	Media           config.MediaConfig

	Now func() time.Time
}
