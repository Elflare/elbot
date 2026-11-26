package maindb

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"elbot/internal/storage/share"
)

func TestCronLastErrorRemovalKeepsScheduleAndDeliveryState(t *testing.T) {
	ctx := context.Background()
	db := OpenBeforeForTest(t, filepath.Join(t.TempDir(), "old.db"), 22)
	now := time.Now()
	next := now.Add(time.Hour)
	state := `{"run_id":"run","report_ready":true,"report":"keep report","targets":[{"key":"cli|superadmins","outputs":[{"id":"text","status":"delivered"}]}]}`
	if _, err := db.Exec(`INSERT INTO cron_jobs(id,name,handler,schedule,enabled,metadata,last_run_at,next_run_at,run_count,last_error,created_at,updated_at,delivery_state,delivery_token) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, "job-id", "job", "handler", "0 8 * * *", 1, `{"keep":true}`, share.FormatTime(now), share.FormatTime(next), 7, "obsolete error", share.FormatTime(now), share.FormatTime(now), state, "token"); err != nil {
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
	job, err := (&Store{db: db}).GetByName(ctx, "job")
	if err != nil {
		t.Fatal(err)
	}
	wantState := `{"report":"keep report","report_ready":true,"run_id":"run","targets":[{"outputs":[{"id":"text","status":"delivered"}],"target":{"platform":"cli","superadmins":true}}]}`
	if job.ID != "job-id" || !job.Enabled || job.Schedule != "0 8 * * *" || job.RunCount != 7 || job.Metadata != `{"keep":true}` || job.DeliveryState != wantState || job.DeliveryToken != "token" || job.LastRunAt == nil || job.NextRunAt == nil || !job.LastRunAt.Equal(now) || !job.NextRunAt.Equal(next) {
		t.Fatalf("migration changed remaining state: %+v", job)
	}
}
