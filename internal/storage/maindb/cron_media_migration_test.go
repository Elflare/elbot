package maindb_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/background/cron"
	"elbot/internal/delivery"
	"elbot/internal/storage"
	"elbot/internal/storage/maindb"
)

func TestCronSourceMigrationPreservesReportsAndUpdatesReferences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.DBForTest()")
	db := maindb.OpenBeforeForTest(t, path, 26)
	jobs := maindb.WithDBForTest(db)
	job, err := jobs.UpsertCronJob(t.Context(), storage.UpsertCronJobRequest{Name: "source-migration", Handler: "test", Schedule: "0 * * * *", Enabled: true})
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
	store, err := maindb.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var persisted string
	if err := store.DBForTest().QueryRow(`SELECT delivery_state FROM cron_jobs WHERE id=?`, job.ID).Scan(&persisted); err != nil || persisted != old {
		t.Fatal(persisted, err)
	}
	id := "media:" + strings.Repeat("a", 64)
	if err := store.UpsertMedia(t.Context(), &storage.Media{ID: id, Backend: "local", MIMEType: "image/png"}); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(cron.CronDeliveryState{RunID: "new", ReportSegments: []delivery.Output{{Kind: delivery.KindImage, Source: delivery.Source{MediaID: id}}}})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := store.CompareAndSwapDelivery(t.Context(), job.ID, "old", "new", string(raw), nil); err != nil || !ok {
		t.Fatal(ok, err)
	}
	refs, err := store.ListByOwner(t.Context(), "cron", job.ID)
	if err != nil || len(refs) != 1 || refs[0].MediaID != id {
		t.Fatal(refs, err)
	}
	if issues, err := store.CheckReferences(t.Context()); err != nil || len(issues) != 0 {
		t.Fatal(issues, err)
	}
}
