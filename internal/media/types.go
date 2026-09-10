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

type Manager struct {
	objects         *sync.Mutex
	Store           storage.Store
	History         storage.ChatHistoryRepository
	Backend         Backend
	Remote          Backend
	MaxImportBytes  int64
	DownloadTimeout time.Duration
	Root            string
	FileDelivery    config.FileDeliveryConfig
	Now             func() time.Time
}
