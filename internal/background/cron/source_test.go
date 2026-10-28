package cron

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	background "elbot/internal/background/share"
	"elbot/internal/delivery"
	"elbot/internal/events"
)

func TestRecoveredReportUsesPersistedSources(t *testing.T) {
	result, err := background.ParseJSONResult(`{"completed":true,"need_report":true,"report_segments":[{"type":"file","url":"HtTpS://example.test/Case?token=Case"},{"type":"file","url":"100%.txt"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	dir := filepath.Join(root, "cron", "test")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "100%.txt"), []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	repo := newFakeCronRepo()
	var sent []delivery.Output
	svc := newTestCronService(t, Options{Store: repo, SandboxRoot: root, SendTarget: func(_ context.Context, _ delivery.Target, outputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
		sent = append(sent, outputs...)
		return delivery.Receipt{}, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
	}})
	svc.now = func() time.Time { return mustParseTestTime(t, "2026-01-02 03:05:00") }
	job := upsertTestCronJob(t, repo, Metadata{Kind: metadataKind, Version: 2, Title: "report", Schedule: CronSchedule{Mode: ScheduleOnce, RunAt: "2026-01-02 03:04:00"}, Trigger: CronTrigger{Mode: TriggerLLM, Message: testElyphTask("test")}, Target: CronTarget{SourcePlatform: "cli"}})
	state := CronDeliveryState{RunID: "run", ReportReady: true, TaskCompleted: true, ReportSegments: result.ReportSegments, ReportSessionID: "session"}
	repo.jobs[job.Name].DeliveryState = mustMarshalTestDelivery(t, state)
	repo.jobs[job.Name].DeliveryToken = "session"
	restored, err := decodeDeliveryState(repo.jobs[job.Name].DeliveryState)
	if err != nil || !reflect.DeepEqual(restored.ReportSegments, result.ReportSegments) {
		t.Fatal(restored, err)
	}
	svc.handlePlatformConnected(t.Context(), "cli")
	if len(sent) != 2 || sent[0].Source.URL != "https://example.test/Case?token=Case" || sent[1].Source.Path != filepath.Join(dir, "100%.txt") {
		t.Fatal(sent)
	}
	if _, err := decodeDeliveryState(`{"report_segments":[{"type":"file","url":"old.txt"}]}`); err == nil {
		t.Fatal("silently accepted old mixed-source storage")
	}
	if strings.Contains(repo.jobs[job.Name].DeliveryState, `"url"`) {
		t.Fatal("persisted external mixed source")
	}
}
