package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"elbot/internal/storage"
)

func TestCronLastErrorRemovalKeepsScheduleAndDeliveryState(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY,name TEXT NOT NULL,applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations {
		if m.version >= 22 {
			break
		}
		if err := applyMigration(ctx, db, m); err != nil {
			t.Fatal(err)
		}
	}
	now := storage.Now()
	next := now.Add(time.Hour)
	state := `{"run_id":"run","report_ready":true,"report":"keep report","targets":[{"key":"cli","outputs":[{"key":"text","status":"delivered"}]}]}`
	if _, err := db.Exec(`INSERT INTO cron_jobs(id,name,handler,schedule,enabled,metadata,last_run_at,next_run_at,run_count,last_error,created_at,updated_at,delivery_state,delivery_token) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, "job-id", "job", "handler", "0 8 * * *", 1, `{"keep":true}`, storage.FormatTime(now), storage.FormatTime(next), 7, "obsolete error", storage.FormatTime(now), storage.FormatTime(now), state, "token"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := runMigrations(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	var columns int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('cron_jobs') WHERE name='last_error'`).Scan(&columns); err != nil || columns != 0 {
		t.Fatalf("last_error columns=%d error=%v", columns, err)
	}
	job, err := (&CronJobRepository{db: db}).GetByName(ctx, "job")
	if err != nil {
		t.Fatal(err)
	}
	if job.ID != "job-id" || !job.Enabled || job.Schedule != "0 8 * * *" || job.RunCount != 7 || job.Metadata != `{"keep":true}` || job.DeliveryState != state || job.DeliveryToken != "token" || job.LastRunAt == nil || job.NextRunAt == nil || !job.LastRunAt.Equal(now) || !job.NextRunAt.Equal(next) {
		t.Fatalf("migration changed remaining state: %+v", job)
	}
}
