package cron

import (
	"strings"
	"testing"

	"elbot/internal/config"
	"elbot/internal/operationresult"
)

func TestCronEmptyTargetsReturnUnavailableBeforeModelAndRunner(t *testing.T) {
	runner := &fakeCronRunner{}
	models := &taskModels{current: config.ModelSelection{Provider: "provider", Model: "model"}}
	svc := newTestCronService(t, Options{Runner: runner, Models: models})
	svc.enabledPlatforms = nil
	_, _, result, err := svc.prepareDelivery(t.Context(), Job{Name: "empty"}, Metadata{Target: CronTarget{AllEnabledPlatforms: true}})
	if err != nil || result.Status != operationresult.Unavailable || runner.calls != 0 || models.resolutions != 0 {
		t.Fatal("result= err= calls= resolutions=", result, err, runner.calls, models.resolutions)
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
			_, result, err := svc.Handler(t.Context(), *job)
			if err != nil || result.Status != operationresult.Unavailable || runner.calls != 0 || models.resolutions != 0 {
				t.Fatal("result= err= calls= resolutions=", result, err, runner.calls, models.resolutions)
			}
			if !strings.Contains(result.Message, "not enabled") || !strings.Contains(result.Message, "telegram") {
				t.Fatalf("result=%+v", result)
			}
			if got := repo.jobs[job.Name]; got.DeliveryState != job.DeliveryState || got.DeliveryToken != job.DeliveryToken {
				t.Fatalf("cancellation wrote delivery state: %+v", got)
			}
		})
	}
}
