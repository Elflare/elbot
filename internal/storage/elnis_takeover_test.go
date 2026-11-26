package storage

import (
	"context"
	"testing"
)

func TestFailInterruptedRollsBackWhenReferenceReleaseFails(t *testing.T) {
	ctx := t.Context()
	store := NewStoreForTest(t)
	if err := store.UpsertMedia(ctx, &Media{ID: "input", Backend: "local", MIMEType: "text/plain"}); err != nil {
		t.Fatal(err)
	}
	repo := store
	event, err := repo.CreateElnisEvent(ctx, CreateElnisEventRequest{ReceivedAt: Now(), CreatedAt: Now(), ID: NewID(),
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
	row, err := repo.GetElnisEvent(ctx, event.ID)
	if err != nil || row.Status != "queued" || row.Error != "" || !row.UpdatedAt.Equal(event.UpdatedAt) {
		t.Fatalf("failed recovery changed event: %#v, %v", row, err)
	}
	refs, err := store.ListByOwner(ctx, "elnis_event", event.ID)
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
	row, err = repo.GetElnisEvent(ctx, event.ID)
	if err != nil || row.Status != "queued" {
		t.Fatalf("empty recovery changed event: %#v, %v", row, err)
	}
	if err := repo.FailInterrupted(ctx, []string{"queued"}, "failed", "test interruption"); err != nil {
		t.Fatal(err)
	}
	row, err = repo.GetElnisEvent(ctx, event.ID)
	if err != nil || row.Status != "failed" || row.Error != "test interruption" {
		t.Fatalf("recovery did not apply caller policy: %#v, %v", row, err)
	}
	refs, err = store.ListByOwner(ctx, "elnis_event", event.ID)
	if err != nil || len(refs) != 0 {
		t.Fatalf("successful recovery retained references: %#v, %v", refs, err)
	}
}

func TestTakenOverEventReleasesEventAndOutboxMedia(t *testing.T) {
	ctx := context.Background()
	store := NewStoreForTest(t)
	if err := store.UpsertMedia(ctx, &Media{ID: "attachment", MIMEType: "image/png", Backend: "local"}); err != nil {
		t.Fatal(err)
	}
	event, err := store.CreateElnisEvent(ctx, CreateElnisEventRequest{ReceivedAt: Now(), CreatedAt: Now(), ID: NewID(), EventKey: "event", ElwispName: "source", SourceID: "1", Status: "running", MediaIDs: []string{"attachment"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PrepareReport(ctx, PrepareElnisReportRequest{EventID: event.ID, ResultReadyStatus: "running", Deliveries: []CreateElnisReportDeliveryRequest{{Target: "{}", Output: `{"Source":{"media":"attachment"}}`}}}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRowContext(ctx, "SELECT count(*) FROM media_references WHERE media_id='attachment'").Scan(&count); err != nil || count != 2 {
		t.Fatalf("references before takeover: %d %v", count, err)
	}
	if err := store.UpdateElnisEvent(ctx, UpdateElnisEventRequest{ID: event.ID, Status: "taken_over"}); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, "SELECT count(*) FROM media_references WHERE media_id='attachment'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("references after takeover: %d %v", count, err)
	}
	issues, err := store.CheckReferences(ctx)
	if err != nil || len(issues) != 0 {
		t.Fatalf("reference check: %#v %v", issues, err)
	}
}
