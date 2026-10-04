package sqlite

import (
	"context"
	"testing"

	"elbot/internal/storage"
)

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
