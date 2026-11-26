package maindb

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"elbot/internal/storage/share"
)

type migration struct {
	version int
	name    string
	sql     string
	apply   func(context.Context, *sql.Tx) error
}

const migrationTableSQL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    applied_at TEXT NOT NULL
);
`

func runMigrations(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, migrationTableSQL); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied, err := appliedMigrations(ctx, db)
	if err != nil {
		return err
	}

	for _, m := range migrations {
		if applied[m.version] {
			continue
		}
		if err := applyMigration(ctx, db, m); err != nil {
			return err
		}
	}
	return nil
}

// migrateStructuredIdentity is the only main-database boundary that reads encoded identities.
func appliedMigrations(ctx context.Context, db *sql.DB) (map[int]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("query schema_migrations: %w", err)
	}
	defer rows.Close()

	applied := map[int]bool{}
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return nil, fmt.Errorf("scan schema_migrations: %w", err)
		}
		applied[version] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate schema_migrations: %w", err)
	}
	return applied, nil
}

func applyMigration(ctx context.Context, db *sql.DB, m migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %d: %w", m.version, err)
	}
	defer tx.Rollback()

	if m.apply != nil {
		if err := m.apply(ctx, tx); err != nil {
			return fmt.Errorf("apply migration %d %s: %w", m.version, m.name, err)
		}
	}
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return fmt.Errorf("apply migration %d %s: %w", m.version, m.name, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations(version, name, applied_at) VALUES (?, ?, ?)`,
		m.version, m.name, share.FormatTime(time.Now()),
	); err != nil {
		return fmt.Errorf("record migration %d %s: %w", m.version, m.name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %d %s: %w", m.version, m.name, err)
	}
	return nil
}
