package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite"
)

// Supervisor owns the two independent databases; consumers only borrow persistence operations.
type Supervisor struct {
	db      *sql.DB    // Main database for sessions, dialogue, media and background jobs.
	history *sql.DB    // Platform chat history; never part of a main-database transaction.
	idMu    sync.Mutex // Protects the reserved general ID range.
	idNext  int64      // Next available number; zero when a new reservation is needed.
	idEnd   int64      // Last number in the current reservation.
}

func NewSupervisor(ctx context.Context, sessionsPath, historyPath string) (*Supervisor, error) {
	db, err := openDB(ctx, sessionsPath)
	if err != nil {
		return nil, fmt.Errorf("open main database: %w", err)
	}
	if err := initializeDB(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize main database: %w", err)
	}
	history, err := openDB(ctx, historyPath)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("chat history build: %w", err)
	}
	if err := migrateChatHistory(ctx, history); err != nil {
		_ = history.Close()
		_ = db.Close()
		return nil, fmt.Errorf("chat history build: %w", err)
	}
	return &Supervisor{db: db, history: history}, nil
}

func (s *Supervisor) Close() error { return errors.Join(s.history.Close(), s.db.Close()) }

func openDB(ctx context.Context, path string) (*sql.DB, error) {
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

	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable sqlite foreign keys: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	return db, nil
}
