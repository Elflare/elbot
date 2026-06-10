package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"elbot/internal/storage"

	_ "modernc.org/sqlite"
)

type Store struct {
	db               *sql.DB
	sessions         *SessionRepository
	messages         *MessageRepository
	contextSummaries *ContextSummaryRepository
	toolCalls        *ToolCallRepository
	cronJobs         *CronJobRepository
}

func New(ctx context.Context, path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("sqlite path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create sqlite directory: %w", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)

	store := &Store{db: db}
	store.sessions = &SessionRepository{db: db}
	store.messages = &MessageRepository{db: db}
	store.contextSummaries = &ContextSummaryRepository{db: db}
	store.toolCalls = &ToolCallRepository{db: db}
	store.cronJobs = &CronJobRepository{db: db}

	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable sqlite foreign keys: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	if err := runMigrations(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}

	return store, nil
}

func (s *Store) Sessions() storage.SessionRepository {
	return s.sessions
}

func (s *Store) Messages() storage.MessageRepository {
	return s.messages
}

func (s *Store) ContextSummaries() storage.ContextSummaryRepository {
	return s.contextSummaries
}

func (s *Store) ToolCalls() storage.ToolCallRepository {
	return s.toolCalls
}

func (s *Store) CronJobs() storage.CronJobRepository {
	return s.cronJobs
}

func (s *Store) Close() error {
	return s.db.Close()
}
