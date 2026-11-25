package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	db, err := openDB(sessionsPath)
	if err != nil {
		return nil, fmt.Errorf("open main database: %w", err)
	}
	if err := initializeDB(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize main database: %w", err)
	}
	history, err := openDB(historyPath)
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

func openDB(path string) (*sql.DB, error) {
	if path == "" {
		return nil, fmt.Errorf("sqlite path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create sqlite directory: %w", err)
	}

	// The driver applies _pragma on every physical connection, including replacements.
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	db, err := sql.Open("sqlite", path+sep+"_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	return db, nil
}
