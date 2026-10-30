package cron

import (
	"context"
	"elbot/internal/events"
	"errors"
	"testing"

	background "elbot/internal/background/share"
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
	svc := newTestCronService(t, Options{Store: repo, Runner: runner, SendTarget: func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, events.LogResult, error) {
		sent++
		return delivery.Receipt{}, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
	}})
	job := upsertTestCronJob(t, repo, Metadata{Kind: metadataKind, Version: 1, Title: "takeover", Schedule: CronSchedule{Mode: ScheduleOnce, RunAt: "2026-01-02 03:04:00"}, Trigger: CronTrigger{Mode: TriggerLLM, Message: testElyphTask("test")}, Target: CronTarget{SourcePlatform: "cli"}})
	if _, err := svc.runTask(context.Background(), *job, mustDecodeTestMetadata(t, job.Metadata)); err != nil {
		t.Fatal(err)
	}
	state := mustDecodeTestDelivery(t, repo.jobs[job.Name].DeliveryState)
	if !state.TakenOver || state.TaskCompleted || state.ExecutionRunID != "logical-run" || state.ExecutionResult != "ordinary foreground answer" || state.ReportMessageID != "actual-answer" {
		t.Fatalf("state: %#v", state)
	}
	if runner.calls != 1 || sent != 0 || repo.jobs[job.Name].Enabled {
		t.Fatalf("calls=%d sent=%d enabled=%v", runner.calls, sent, repo.jobs[job.Name].Enabled)
	}
	svc.handlePlatformConnected(context.Background(), "cli")
	if sent != 0 || runner.calls != 1 {
		t.Fatal("takeover was retried on reconnect")
	}
}

func TestInjectedTakeoverQuerySuppressesRepairAndSurfacesFailure(t *testing.T) {
	failure := errors.New("takeover lookup failed")
	for _, lookupErr := range []error{nil, failure} {
		runner := &fakeCronRunner{text: "invalid JSON"}
		queried := ""
		svc := newTestCronService(t, Options{Runner: runner, SessionTakenOver: func(_ context.Context, id string) (bool, error) { queried = id; return lookupErr == nil, lookupErr }})
		state, _, err := svc.runLLMReport(t.Context(), Job{Name: "job"}, Metadata{}, CronDeliveryState{RunID: "run"})
		if runner.calls != 1 || queried != "cron-session" || state.Report != "" {
			t.Fatalf("calls=%d query=%q report=%q", runner.calls, queried, state.Report)
		}
		if lookupErr != nil {
			if !errors.Is(err, lookupErr) {
				t.Fatalf("error=%v", err)
			}
		} else if err != nil || !state.TakenOver {
			t.Fatalf("state=%#v error=%v", state, err)
		}
	}
}

func TestInjectedTakeoverQueryStopsPreparedDelivery(t *testing.T) {
	repo := newFakeCronRepo()
	sent := 0
	svc := newTestCronService(t, Options{Store: repo, SessionTakenOver: func(_ context.Context, id string) (bool, error) { return id == "adopted", nil }, SendTarget: func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, events.LogResult, error) {
		sent++
		return delivery.Receipt{}, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
	}})
	job := upsertTestCronJob(t, repo, Metadata{Kind: metadataKind, Schedule: CronSchedule{Mode: ScheduleOnce}, Target: CronTarget{SourcePlatform: "cli"}})
	state := CronDeliveryState{RunID: "run", ReportReady: true, NeedReport: true, Report: "unsent", ReportSessionID: "adopted"}
	repo.jobs[job.Name].DeliveryToken = "run"
	job.DeliveryToken = "run"
	if _, err := svc.deliverPrepared(t.Context(), *job, mustDecodeTestMetadata(t, job.Metadata), state, "", false); err != nil {
		t.Fatal(err)
	}
	if sent != 0 {
		t.Fatalf("sent=%d", sent)
	}
	stored, _ := repo.GetByName(t.Context(), job.Name)
	if stored.Enabled || !mustDecodeTestDelivery(t, stored.DeliveryState).TakenOver {
		t.Fatalf("state=%#v", stored)
	}
}
