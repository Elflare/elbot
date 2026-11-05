package cron

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/signal"
)

func TestFormatRetryExecutionFailureStopsTask(t *testing.T) {
	for _, failure := range []error{errors.New("model failed"), errors.New("save failed"), context.Canceled} {
		t.Run(failure.Error(), func(t *testing.T) {
			repo := newFakeCronRepo()
			runner := &fakeCronRunner{text: "invalid json", errors: []error{nil, failure}}
			sent := 0
			svc := newTestCronService(t, Options{Store: repo, Runner: runner, SendTarget: func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, error) {
				sent++
				return delivery.Receipt{}, nil
			}})
			meta := Metadata{Kind: metadataKind, Version: 1, Title: "retry", Schedule: CronSchedule{Mode: ScheduleCron}, Trigger: CronTrigger{Mode: TriggerLLM, Message: testElyphTask("retry")}, Target: CronTarget{SourcePlatform: "cli"}}
			job := upsertTestCronJob(t, repo, meta)
			_, err := svc.runTask(t.Context(), *job, meta)
			if err != failure || runner.calls != 2 || sent != 0 {
				t.Fatalf("err=%v calls=%d sent=%d", err, runner.calls, sent)
			}
			stored, err := repo.GetByName(t.Context(), job.Name)
			if err != nil {
				t.Fatal(err)
			}
			state, err := decodeDeliveryState(stored.DeliveryState)
			if err != nil || state.ReportReady || strings.Contains(state.Report, "解析格式失败") {
				t.Fatalf("execution failure became a format report: %+v err=%v", state, err)
			}
		})
	}
}

type failingNextRunRepo struct {
	*fakeCronRepo
	err error
}

func (r failingNextRunRepo) UpdateNextRunAt(context.Context, string, *time.Time, time.Time) error {
	return r.err
}

func TestReloadReportsNextRunSaveFailureOnce(t *testing.T) {
	failure := errors.New("next run save failed")
	repo := failingNextRunRepo{newFakeCronRepo(), failure}
	next := time.Now()
	if _, err := repo.Upsert(t.Context(), UpsertJobRequest{Name: "job", Enabled: true, NextRunAt: &next}); err != nil {
		t.Fatal(err)
	}
	m := NewManager(repo)
	m.started = true
	var records []events.LogRecord
	connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
		if strings.Contains(record.Content, failure.Error()) {
			records = append(records, record)
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	if err := m.reloadEnabled(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].EventName != "cron_job_schedule_failed" {
		t.Fatalf("duplicate or missing save failure: %+v", records)
	}
}
