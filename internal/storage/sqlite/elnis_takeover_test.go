package sqlite

import (
	"context"
	"testing"

	"elbot/internal/storage"
)

func TestFailInterruptedRollsBackWhenReferenceReleaseFails(t *testing.T) {
	ctx := t.Context()
	store := newTestStore(t)
	if err := store.Media().Upsert(ctx, &storage.Media{ID: "input", Backend: "local", MIMEType: "text/plain"}); err != nil {
		t.Fatal(err)
	}
	repo := store.ElnisEvents()
	event, err := repo.Create(ctx, storage.CreateElnisEventRequest{
		EventKey: "interrupted", ElwispName: "source", SourceID: "interrupted",
		Status: "queued", MediaIDs: []string{"input"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `CREATE TRIGGER reject_recovery_release BEFORE DELETE ON media_references
		WHEN OLD.owner_type='elnis_event' BEGIN SELECT RAISE(ABORT,'reference release failed'); END`); err != nil {
		t.Fatal(err)
	}
	if err := repo.FailInterrupted(ctx, []string{"queued"}, "failed", "test interruption"); err == nil {
		t.Fatal("expected reference release failure")
	}
	row, err := repo.Get(ctx, event.ID)
	if err != nil || row.Status != "queued" || row.Error != "" || !row.UpdatedAt.Equal(event.UpdatedAt) {
		t.Fatalf("failed recovery changed event: %#v, %v", row, err)
	}
	refs, err := store.MediaReferences().ListByOwner(ctx, "elnis_event", event.ID)
	if err != nil || len(refs) != 1 {
		t.Fatalf("failed recovery released references: %#v, %v", refs, err)
	}
	if _, err := store.db.ExecContext(ctx, "DROP TRIGGER reject_recovery_release"); err != nil {
		t.Fatal(err)
	}
	// An empty source set must not update every event.
	if err := repo.FailInterrupted(ctx, nil, "failed", "must not apply"); err != nil {
		t.Fatal(err)
	}
	row, err = repo.Get(ctx, event.ID)
	if err != nil || row.Status != "queued" {
		t.Fatalf("empty recovery changed event: %#v, %v", row, err)
	}
	if err := repo.FailInterrupted(ctx, []string{"queued"}, "failed", "test interruption"); err != nil {
		t.Fatal(err)
	}
	row, err = repo.Get(ctx, event.ID)
	if err != nil || row.Status != "failed" || row.Error != "test interruption" {
		t.Fatalf("recovery did not apply caller policy: %#v, %v", row, err)
	}
	refs, err = store.MediaReferences().ListByOwner(ctx, "elnis_event", event.ID)
	if err != nil || len(refs) != 0 {
		t.Fatalf("successful recovery retained references: %#v, %v", refs, err)
	}
}

func TestTakenOverEventReleasesEventAndOutboxMedia(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	if err := store.Media().Upsert(ctx, &storage.Media{ID: "attachment", MIMEType: "image/png", Backend: "local"}); err != nil {
		t.Fatal(err)
	}
	event, err := store.ElnisEvents().Create(ctx, storage.CreateElnisEventRequest{EventKey: "event", ElwispName: "source", SourceID: "1", Status: "running", MediaIDs: []string{"attachment"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ElnisEvents().PrepareReport(ctx, storage.PrepareElnisReportRequest{EventID: event.ID, ResultReadyStatus: "running", Deliveries: []storage.CreateElnisReportDeliveryRequest{{Target: "{}", Output: `{"Source":{"media":"attachment"}}`}}}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRowContext(ctx, "SELECT count(*) FROM media_references WHERE media_id='attachment'").Scan(&count); err != nil || count != 2 {
		t.Fatalf("references before takeover: %d %v", count, err)
	}
	if err := store.ElnisEvents().Update(ctx, storage.UpdateElnisEventRequest{ID: event.ID, Status: "taken_over"}); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, "SELECT count(*) FROM media_references WHERE media_id='attachment'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("references after takeover: %d %v", count, err)
	}
	issues, err := store.Media().CheckReferences(ctx)
	if err != nil || len(issues) != 0 {
		t.Fatalf("reference check: %#v %v", issues, err)
	}
}
