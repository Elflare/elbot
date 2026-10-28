package sqlite

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/background/cron"
	"elbot/internal/delivery"
	"elbot/internal/storage"
)

func TestCronSourceMigrationPreservesReportsAndUpdatesReferences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY,name TEXT NOT NULL,applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations {
		if m.version > 25 {
			break
		}
		if err := applyMigration(t.Context(), db, m); err != nil {
			t.Fatal(err)
		}
	}
	jobs := &CronJobRepository{db: db}
	job, err := jobs.Upsert(t.Context(), storage.UpsertCronJobRequest{Name: "source-migration", Handler: "test", Schedule: "0 * * * *", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	old := `{"run_id":"old","report_segments":[{"type":"file","url":"kept.txt"}]}`
	if ok, err := jobs.CompareAndSwapDelivery(t.Context(), job.ID, "", "old", old, nil); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := New(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var persisted string
	if err := store.db.QueryRow(`SELECT delivery_state FROM cron_jobs WHERE id=?`, job.ID).Scan(&persisted); err != nil || persisted != old {
		t.Fatal(persisted, err)
	}
	id := "media:" + strings.Repeat("a", 64)
	if err := store.Media().Upsert(t.Context(), &storage.Media{ID: id, Backend: "local", MIMEType: "image/png"}); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(cron.CronDeliveryState{RunID: "new", ReportSegments: []delivery.Output{{Kind: delivery.KindImage, Source: delivery.Source{MediaID: id}}}})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := store.CronJobs().CompareAndSwapDelivery(t.Context(), job.ID, "old", "new", string(raw), nil); err != nil || !ok {
		t.Fatal(ok, err)
	}
	refs, err := store.MediaReferences().ListByOwner(t.Context(), "cron", job.ID)
	if err != nil || len(refs) != 1 || refs[0].MediaID != id {
		t.Fatal(refs, err)
	}
	if issues, err := store.Media().CheckReferences(t.Context()); err != nil || len(issues) != 0 {
		t.Fatal(issues, err)
	}
}
