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
	_, state, err := svc.prepareDelivery(t.Context(), Job{Name: "empty"}, Metadata{Target: CronTarget{AllEnabledPlatforms: true}})
	if err == nil || runner.calls != 0 || models.resolutions != 0 || state.ReportSessionID != "" {
		t.Fatal("state= result= err= calls= resolutions=", state, err, runner.calls, models.resolutions)
	}
}

func TestCronUnconfiguredTargetCancelsAndLogsBeforeDeliveryState(t *testing.T) {
	for _, mode := range []TriggerMode{TriggerLLM, TriggerDirect} {
		t.Run(string(mode), func(t *testing.T) {
			repo := newFakeCronRepo()
			runner := &fakeCronRunner{}
			models := &taskModels{current: config.ModelSelection{Provider: "provider", Model: "model"}}
			svc := newTestCronService(t, Options{Store: repo, Runner: runner, Models: models})
			job := upsertTestCronJob(t, repo, Metadata{Kind: metadataKind, Version: 1, Title: "disabled target", Schedule: CronSchedule{Mode: ScheduleOnce, RunAt: "2026-01-02 03:04:00"}, Trigger: CronTrigger{Mode: mode, Message: "test"}, Target: CronTarget{SourcePlatform: "telegram"}})
			var records []events.LogRecord
			connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
				if record.EventName == "cron.trigger_failed" {
					records = append(records, record)
				}
				return nil
			}, signal.ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Disconnect()
			err = svc.Handler(t.Context(), *job)
			if err == nil || runner.calls != 0 || models.resolutions != 0 {
				t.Fatal("result= err= calls= resolutions=", err, runner.calls, models.resolutions)
			}
			if len(records) != 1 || records[0].ResultStatus != events.ResultFailed || !strings.Contains(records[0].Content, "not enabled") || !strings.Contains(records[0].Content, "telegram") {
				t.Fatalf("logs=%+v", records)
			}
			if got := repo.jobs[job.Name]; got.DeliveryState != job.DeliveryState || got.DeliveryToken != job.DeliveryToken {
				t.Fatalf("cancellation wrote delivery state: %+v", got)
			}
		})
	}
}
