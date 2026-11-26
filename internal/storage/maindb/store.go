package maindb

import (
	"context"
	"database/sql"
	"fmt"
	"sync"

	"elbot/internal/storage/share"
)

type Store struct {
	db     *sql.DB    // Main database for sessions, dialogue, media and background jobs.
	idMu   sync.Mutex // Protects the reserved general ID range.
	idNext int64      // Next available number; zero when a new reservation is needed.
	idEnd  int64      // Last number in the current reservation.
}

func Open(ctx context.Context, path string) (*Store, error) {
	db, err := share.OpenDB(path)
	if err != nil {
		return nil, fmt.Errorf("open main database: %w", err)
	}
	if err := initializeDB(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize main database: %w", err)
	}
	return &Store{db: db}, nil
}
func (s *Store) Close() error { return s.db.Close() }

// requireRow distinguishes a missing row from a failed write or unavailable row count.
func requireRow(res sql.Result, err error, operation string) error {
	if err == nil {
		var n int64
		n, err = res.RowsAffected()
		if err == nil && n == 0 {
			return share.ErrNotFound
		}
	}
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return nil
}
