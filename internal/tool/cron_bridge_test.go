package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
	"elbot/internal/tool/builtin"
	"elbot/internal/tool/runtimeinfo"
	"elbot/internal/tool/share"
)

func TestCronBridgeStorageAndNotifications(t *testing.T) {
	store, err := sqlite.New(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	changed := signal.New[CronChangedEvent, signal.Empty]("test.cron_changed")
	defer changed.Close(context.Background())
	bridge := cronBridge{repo: store.CronJobs(), changed: changed}
	owner := contextinfo.Actor{ID: "owner", Platform: "qq", PlatformUserID: "123", DisplayName: "Owner"}
	ctx := contextinfo.WithActor(t.Context(), owner)
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.Local)
	tools := builtin.NewCronTools(bridge, nil, runtimeinfo.Info{Now: func() time.Time { return now }})
	var notifications []CronChangedEvent
	var exists []bool
	_, err = (&Supervisor{cronChanged: changed}).ObserveCronChanged(func(event CronChangedEvent) error {
		_, err := store.CronJobs().GetByName(context.Background(), event.Name)
		if err != nil && !errors.Is(err, storage.ErrNotFound) {
			return err
		}
		notifications = append(notifications, event)
		exists = append(exists, err == nil)
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	write := func(args string) {
		t.Helper()
		if _, _, err := callTool(ctx, tools[2], share.CallRequest{Arguments: json.RawMessage(args)}); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"operation":"create","name":"reminder","schedule_mode":"once","run_after_minutes":10,"trigger_mode":"llm","message":"#task reminder - test"}`)
	job, err := store.CronJobs().GetByName(ctx, "user.cron.reminder")
	if err != nil {
		t.Fatal(err)
	}
	var meta storage.CronMetadata
	if err := json.Unmarshal([]byte(job.Metadata), &meta); err != nil {
		t.Fatal(err)
	}
	if job.Handler != storage.CronHandlerName || meta.CreatedBy.ActorID != owner.ID || meta.CreatedBy.PlatformUserID != owner.PlatformUserID || meta.Schedule.RunAt != "2026-10-25 12:10:00" || meta.Kind != storage.CronMetadataKind {
		t.Fatalf("persisted = %+v %+v", job, meta)
	}
	if ok, err := store.CronJobs().CompareAndSwapDelivery(ctx, job.ID, job.DeliveryToken, "delivery", `{"delivery_complete":true}`, nil); err != nil || !ok {
		t.Fatal(ok, err)
	}
	info, err := bridge.GetByName(ctx, "reminder")
	if err != nil || !info.Completed || info.CreatedBy.ID != owner.ID {
		t.Fatalf("read = %+v %v", info, err)
	}
	result, _, err := callTool(ctx, tools[1], share.CallRequest{})
	if err != nil || result.Content != "[]" {
		t.Fatalf("filtered = %+v %v", result, err)
	}
	result, _, err = callTool(ctx, tools[1], share.CallRequest{Arguments: json.RawMessage(`{"include_completed":true}`)})
	if err != nil || !result.NeedsElyphRules || strings.Contains(result.Content, "delivery_complete") {
		t.Fatalf("query = %+v %v", result, err)
	}
	write(`{"operation":"update","name":"reminder","title":"renamed"}`)
	job, _ = store.CronJobs().GetByName(ctx, job.Name)
	if job.DeliveryToken != "delivery" {
		t.Fatal("title edit reset delivery")
	}
	write(`{"operation":"update","name":"reminder","enabled":true}`)
	job, _ = store.CronJobs().GetByName(ctx, job.Name)
	if job.DeliveryState != "" || job.DeliveryToken == "delivery" {
		t.Fatal("reenable kept stale delivery")
	}
	write(`{"operation":"disable","name":"reminder"}`)
	info, err = bridge.GetByName(ctx, "reminder")
	if err != nil || info.Enabled || info.NextRunAt != nil {
		t.Fatalf("disabled = %+v %v", info, err)
	}
	write(`{"operation":"delete","name":"reminder"}`)
	if len(notifications) != 5 || !exists[0] || !exists[1] || !exists[2] || !exists[3] || exists[4] {
		t.Fatalf("commit observations = %+v %v", notifications, exists)
	}
	for _, event := range notifications {
		if event.Name != "user.cron.reminder" {
			t.Fatal(event)
		}
	}
	if err := bridge.DisableByName(ctx, "missing"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := bridge.Upsert(canceled, builtin.CronSaveRequest{Definition: builtin.CronCreateRequest{Name: "fail"}}); err == nil {
		t.Fatal("canceled write succeeded")
	}
	if len(notifications) != 5 {
		t.Fatal("failed write or query emitted a change")
	}
}

func TestCronBridgeFiltersStorageRecordsAndReportsCommittedNotifyFailure(t *testing.T) {
	store, err := sqlite.New(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	changed := signal.New[CronChangedEvent, signal.Empty]("test.cron_changed")
	bridge := cronBridge{repo: store.CronJobs(), changed: changed}
	if _, err := store.CronJobs().Upsert(t.Context(), storage.UpsertCronJobRequest{Name: "maintenance", Handler: "maintenance", Schedule: "* * * * *", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	items, err := bridge.List(t.Context(), true)
	if err != nil || len(items) != 0 {
		t.Fatal(items, err)
	}
	if err := changed.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err = bridge.Upsert(t.Context(), builtin.CronSaveRequest{Definition: builtin.CronCreateRequest{Name: "saved"}, Schedule: "* * * * *"})
	if err == nil || !strings.Contains(err.Error(), "已写入") {
		t.Fatalf("notification error = %v", err)
	}
	if _, err := store.CronJobs().GetByName(t.Context(), "user.cron.saved"); err != nil {
		t.Fatal("successful write was lost", err)
	}
}
