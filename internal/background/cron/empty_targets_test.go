package cron

import (
	"context"
	"strings"
	"testing"

	"elbot/internal/config"
	"elbot/internal/events"
	"elbot/internal/signal"
)

func TestCronEmptyTargetsCancelBeforeModelAndRunner(t *testing.T) {
	runner := &fakeCronRunner{}
	models := &taskModels{current: config.ModelSelection{Provider: "provider", Model: "model"}}
	svc := newTestCronService(t, Options{Runner: runner, Models: models})
	svc.enabledPlatforms = nil
	_, _, state, err := svc.prepareDelivery(t.Context(), Job{Name: "empty"}, Metadata{Target: CronTarget{AllEnabledPlatforms: true}})
	if err == nil || runner.calls != 0 || models.resolutions != 0 || state.ReportSessionID != "" {
		t.Fatal("state= result= err= calls= resolutions=", state, err, runner.calls, models.resolutions)
	}
}

func TestCronUnconfiguredTargetReturnsBeforeDeliveryState(t *testing.T) {
	for _, mode := range []TriggerMode{TriggerLLM, TriggerDirect} {
		t.Run(string(mode), func(t *testing.T) {
			repo := newFakeCronRepo()
			runner := &fakeCronRunner{}
			models := &taskModels{current: config.ModelSelection{Provider: "provider", Model: "model"}}
			svc := newTestCronService(t, Options{Store: repo, Runner: runner, Models: models})
			job := upsertTestCronJob(t, repo, Metadata{Kind: metadataKind, Version: 1, Title: "disabled target", Schedule: CronSchedule{Mode: ScheduleOnce, RunAt: "2026-01-02 03:04:00"}, Trigger: CronTrigger{Mode: mode, Message: "test"}, Target: CronTarget{SourcePlatform: "telegram"}})
			var records []events.LogRecord
			connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) {
				if record.EventName == "cron.trigger_failed" {
					records = append(records, record)
				}

			}, signal.ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Disconnect()
			_, err = svc.Handler(t.Context(), *job)
			if err == nil || runner.calls != 0 || models.resolutions != 0 {
				t.Fatal("result= err= calls= resolutions=", err, runner.calls, models.resolutions)
			}
			if len(records) != 0 || !strings.Contains(err.Error(), "not enabled") || !strings.Contains(err.Error(), "telegram") {
				t.Fatalf("logs=%+v", records)
			}
			if got := repo.jobs[job.Name]; got.DeliveryState != job.DeliveryState || got.DeliveryToken != job.DeliveryToken {
				t.Fatalf("cancellation wrote delivery state: %+v", got)
			}
		})
	}
}
