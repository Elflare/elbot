// Package testutil provides fixtures shared by repository tests.
package testutil

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"elbot/internal/storage/sqlite"

	sqlitedriver "modernc.org/sqlite"
)

var sqliteImage = sync.OnceValues(newSQLiteImage)
var sqliteSequence atomic.Uint64

type sqliteSnapshotConnection interface {
	Serialize() ([]byte, error)
	Deserialize([]byte) error
	NewBackup(string) (*sqlitedriver.Backup, error)
}

// NewSQLiteStore clones the migrated empty schema into an independent memory
// database. Tests of migrations or persistence across reopening should use sqlite.New.
func NewSQLiteStore(t *testing.T) *sqlite.Store {
	t.Helper()
	image, err := sqliteImage()
	if err != nil {
		t.Fatalf("prepare SQLite test image: %v", err)
	}
	ctx := context.Background()
	source, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	conn, err := source.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	dsn := fmt.Sprintf("file:elbot-testutil-%d?mode=memory&cache=shared", sqliteSequence.Add(1))
	var store *sqlite.Store
	err = conn.Raw(func(driverConn any) error {
		snapshot := driverConn.(sqliteSnapshotConnection)
		if err := snapshot.Deserialize(image); err != nil {
			return err
		}
		backup, err := snapshot.NewBackup(dsn)
		if err != nil {
			return err
		}
		if _, err := backup.Step(-1); err != nil {
			_ = backup.Finish()
			return err
		}
		keepAlive, err := backup.Commit()
		if err != nil {
			return err
		}
		// The clone must stay open until Store owns a connection to it.
		defer keepAlive.Close()
		store, err = sqlite.New(ctx, dsn)
		return err
	})
	if err != nil {
		t.Fatalf("clone SQLite test database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func newSQLiteImage() ([]byte, error) {
	ctx := context.Background()
	const dsn = "file:elbot-testutil-template?mode=memory&cache=shared"
	seed, err := sqlite.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	defer seed.Close()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	var image []byte
	err = conn.Raw(func(driverConn any) error {
		image, err = driverConn.(sqliteSnapshotConnection).Serialize()
		return err
	})
	return image, err
}
