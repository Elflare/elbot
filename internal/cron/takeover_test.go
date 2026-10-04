package cron

import (
	"context"
	"testing"

	"elbot/internal/background"
	"elbot/internal/delivery"
)

type takeoverRunner struct{ calls int }

func (r *takeoverRunner) RunBackground(context.Context, background.RunRequest) (background.RunResult, error) {
	r.calls++
	return background.RunResult{RunID: "logical-run", SessionID: "adopted", MessageID: "actual-answer", Text: "ordinary foreground answer", TakenOver: true, Outcome: "completed"}, nil
}

func TestTakeoverSuppressesRepairAndDelivery(t *testing.T) {
	repo := newFakeCronRepo()
	runner := &takeoverRunner{}
	sent := 0
	svc := NewService(Options{Store: fakeCronStore{cron: repo}, Runner: runner, SendTarget: func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, error) {
		sent++
		return delivery.Receipt{}, nil
	}})
	job := upsertTestCronJob(t, repo, Metadata{Kind: metadataKind, Version: 1, Title: "takeover", Schedule: CronSchedule{Mode: ScheduleOnce, RunAt: "2026-01-02 03:04:00"}, Trigger: CronTrigger{Mode: TriggerLLM, Message: testElyphTask("test")}, Target: CronTarget{SourcePlatform: "cli"}})
	if err := svc.runLLM(context.Background(), *job, mustDecodeTestMetadata(t, job.Metadata)); err != nil {
		t.Fatal(err)
	}
	state := mustDecodeTestDelivery(t, repo.jobs[job.Name].DeliveryState)
	if !state.TakenOver || state.TaskCompleted || state.ExecutionRunID != "logical-run" || state.ExecutionResult != "ordinary foreground answer" || state.ReportMessageID != "actual-answer" {
		t.Fatalf("state: %#v", state)
	}
	if runner.calls != 1 || sent != 0 || repo.jobs[job.Name].Enabled {
		t.Fatalf("calls=%d sent=%d enabled=%v", runner.calls, sent, repo.jobs[job.Name].Enabled)
	}
	svc.NotifyPlatformConnected(context.Background(), "cli")
	if sent != 0 || runner.calls != 1 {
		t.Fatal("takeover was retried on reconnect")
	}
}
