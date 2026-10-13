package sqlite

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"elbot/internal/cron"
	"elbot/internal/llm"
	"elbot/internal/storage"
)

func TestMediaAuditDetectsMissingCronReference(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	id := "media:" + strings.Repeat("c", 64)
	if err := store.Media().Upsert(ctx, &storage.Media{ID: id, MIMEType: "image/png", Backend: "local"}); err != nil {
		t.Fatal(err)
	}
	job, err := store.CronJobs().Upsert(ctx, storage.UpsertCronJobRequest{Name: "audit-media", Handler: "test", Schedule: "0 * * * *", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(cron.CronDeliveryState{RunID: "run", ReportSegments: []llm.MessageSegment{{Type: llm.SegmentImage, MediaID: id}}})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := store.CronJobs().CompareAndSwapDelivery(ctx, job.ID, "", "run", string(data)); err != nil || !ok {
		t.Fatalf("save report: %v %v", ok, err)
	}
	if issues, err := store.Media().CheckReferences(ctx); err != nil || len(issues) != 0 {
		t.Fatalf("valid references: %v %v", issues, err)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM media_references WHERE owner_type='cron' AND owner_id=?`, job.ID); err != nil {
		t.Fatal(err)
	}
	issues, err := store.Media().CheckReferences(ctx)
	if err != nil || len(issues) != 1 || issues[0] != "issue=missing_reference owner_type=cron owner_id="+job.ID+" media_id="+id {
		t.Fatalf("missing cron reference not detected: %v %v", issues, err)
	}
}
func TestMediaJSONReferenceTransactions(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ids := []string{"media:" + strings.Repeat("a", 64), "media:" + strings.Repeat("b", 64)}
	for _, id := range ids {
		if err := store.Media().Upsert(ctx, &storage.Media{ID: id, MIMEType: "image/png", Backend: "local"}); err != nil {
			t.Fatal(err)
		}
	}
	segments := func(id string) string {
		t.Helper()
		data, err := json.Marshal([]llm.MessageSegment{{Type: llm.SegmentImage, MediaID: id}})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), `"media":`) || strings.Contains(string(data), "media_id") {
			t.Fatalf("wrong media JSON: %s", data)
		}
		return string(data)
	}
	assertOwner := func(kind, owner, id string) {
		t.Helper()
		refs, err := store.MediaReferences().ListByOwner(ctx, kind, owner)
		if err != nil {
			t.Fatal(err)
		}
		if id == "" {
			if len(refs) != 0 {
				t.Fatalf("unexpected references: %v", refs)
			}
		} else if len(refs) != 1 || refs[0].MediaID != id {
			t.Fatalf("references = %v, want %s", refs, id)
		}
	}

	session := &storage.Session{OwnerID: "u", Platform: "test", PlatformScopeID: "s"}
	if err := store.Sessions().Create(ctx, session); err != nil {
		t.Fatal(err)
	}
	msg := &storage.Message{SessionID: session.ID, Role: storage.RoleUser, Segments: segments(ids[0])}
	if err := store.Messages().Append(ctx, msg); err != nil {
		t.Fatal(err)
	}
	assertOwner("message", msg.ID, ids[0])
	if _, err := store.db.ExecContext(ctx, `UPDATE messages SET segments=? WHERE id=?`, segments(ids[1]), msg.ID); err != nil {
		t.Fatal(err)
	}
	assertOwner("message", msg.ID, ids[1])
	if _, err := store.db.ExecContext(ctx, `UPDATE messages SET segments=? WHERE id=?`, segments("media:missing"), msg.ID); err == nil {
		t.Fatal("accepted missing message media")
	}
	persisted, err := store.Messages().Get(ctx, msg.ID)
	if err != nil || persisted.Segments != segments(ids[1]) {
		t.Fatalf("message replacement did not roll back: %v %v", persisted, err)
	}
	assertOwner("message", msg.ID, ids[1])

	job, err := store.CronJobs().Upsert(ctx, storage.UpsertCronJobRequest{Name: "media-json", Handler: "test", Schedule: "0 * * * *", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	state := func(id string) string {
		t.Helper()
		data, err := json.Marshal(cron.CronDeliveryState{RunID: "run", ReportReady: true, ReportSegments: []llm.MessageSegment{{Type: llm.SegmentImage, MediaID: id}}})
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	if ok, err := store.CronJobs().CompareAndSwapDelivery(ctx, job.ID, "", "first", state(ids[0])); err != nil || !ok {
		t.Fatalf("create delivery: %v %v", ok, err)
	}
	assertOwner("cron", job.ID, ids[0])
	if ok, err := store.CronJobs().CompareAndSwapDelivery(ctx, job.ID, "first", "second", state(ids[1])); err != nil || !ok {
		t.Fatalf("replace delivery: %v %v", ok, err)
	}
	assertOwner("cron", job.ID, ids[1])
	if _, err := store.CronJobs().CompareAndSwapDelivery(ctx, job.ID, "second", "bad", state("media:missing")); err == nil {
		t.Fatal("accepted missing cron media")
	}
	assertOwner("cron", job.ID, ids[1])
	if ok, err := store.CronJobs().CompareAndSwapDelivery(ctx, job.ID, "second", "", ""); err != nil || !ok {
		t.Fatalf("delivery token was not rolled back: %v %v", ok, err)
	}
	assertOwner("cron", job.ID, "")
}

func TestNativePublicMaterialsRetainMediaAndAuditMissingReferences(t *testing.T) {
	ctx := t.Context()
	store := newTestStore(t)
	id := "media:" + strings.Repeat("d", 64)
	if err := store.Media().Upsert(ctx, &storage.Media{ID: id, MIMEType: "image/png", Backend: "local"}); err != nil {
		t.Fatal(err)
	}
	row := &storage.Session{OwnerID: "u", Platform: "test"}
	if err := store.Sessions().Create(ctx, row); err != nil {
		t.Fatal(err)
	}
	input := storage.NativeInput{ID: "input", SessionID: row.ID, ItemJSON: `{"type":"message","role":"user","content":[]}`, MediaJSON: `[{"index":0,"segments":[{"type":"image","media":"` + id + `"}]}]`}
	if _, err := store.Dialogues().Commit(ctx, storage.DialogueCommit{SessionID: row.ID, Native: &storage.NativeCommit{Inputs: []storage.NativeInput{input}}}); err != nil {
		t.Fatal(err)
	}
	refs, err := store.MediaReferences().ListByOwner(ctx, "native_input", input.ID)
	if err != nil || len(refs) != 1 || refs[0].MediaID != id {
		t.Fatalf("refs=%+v err=%v", refs, err)
	}
	if issues, err := store.Media().CheckReferences(ctx); err != nil || len(issues) != 0 {
		t.Fatalf("issues=%v err=%v", issues, err)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM media_references WHERE owner_type='native_input' AND owner_id=?`, input.ID); err != nil {
		t.Fatal(err)
	}
	issues, err := store.Media().CheckReferences(ctx)
	if err != nil || len(issues) != 1 || !strings.Contains(issues[0], "owner_type=native_input") {
		t.Fatalf("missing reference ignored: %v %v", issues, err)
	}
}
