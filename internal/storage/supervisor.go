package storage

import (
	"context"
	"errors"
	"fmt"

	"elbot/internal/storage/historydb"
	"elbot/internal/storage/maindb"
	"elbot/internal/storage/share"
)

var ErrNotFound = share.ErrNotFound

type mainStore = maindb.Store
type historyStore = historydb.Store

// Supervisor owns both databases; only persistence operations are promoted.
type Supervisor struct {
	*mainStore
	*historyStore
}

func NewSupervisor(ctx context.Context, sessionsPath, historyPath string) (*Supervisor, error) {
	main, err := maindb.Open(ctx, sessionsPath)
	if err != nil {
		return nil, err
	}
	history, err := historydb.Open(ctx, historyPath)
	if err != nil {
		_ = main.Close()
		return nil, fmt.Errorf("chat history build: %w", err)
	}
	return &Supervisor{main, history}, nil
}

func (s *Supervisor) Close() error { return errors.Join(s.historyStore.Close(), s.mainStore.Close()) }
