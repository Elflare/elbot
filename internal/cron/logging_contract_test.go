package cron

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
	"elbot/internal/signal"
	"elbot/internal/storage"
)

type loggingCronRepo struct {
	*fakeCronRepo
	saveErr error
	saved   bool
}

func TestDeliveryUsesSourceFailureWithSuccessfulBusinessReturn(t *testing.T) {
	ctx := context.Background()
	repo := newFakeCronRepo()
	meta := Metadata{Kind: metadataKind, Version: 1, Title: "partial", Schedule: CronSchedule{Mode: ScheduleCron}, Trigger: CronTrigger{Mode: TriggerDirect, Message: "report"}, Target: CronTarget{SourcePlatform: "cli"}}
	job := upsertTestCronJob(t, repo, meta)
	s := NewService(Options{Store: fakeCronStore{cron: repo}, SendTarget: func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
		return delivery.Receipt{PlatformMessageIDs: []string{"received"}}, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: "one destination failed"}, nil
	}})
	var records []globalevents.LogRecord
	connection, err := globalevents.LogSubmitted.Observe(func(_ context.Context, record globalevents.LogRecord) error {
		if record.EventName == "cron.send_failed" || record.EventName == "cron.send_completed" {
			records = append(records, record)
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(connection.Disconnect)
	logResult, err := s.runDirect(ctx, *job, meta)
	if err != nil || logResult.ResultStatus != globalevents.ResultFailed || !strings.Contains(logResult.Content, "one destination failed") {
		t.Fatalf("log result=%#v error=%v", logResult, err)
	}
	if len(records) != 1 || records[0].ResultStatus != globalevents.ResultFailed || records[0].Level != slog.LevelError {
		t.Fatalf("source failure ignored: %#v", records)
	}
}

func TestHandlerUsesDeclaredDeliveryResult(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status globalevents.ResultStatus
		err    error
	}{
		{"failed_without_business_error", globalevents.ResultFailed, nil},
		{"canceled_without_business_error", globalevents.ResultCanceled, nil},
		{"mixed_failure", globalevents.ResultFailed, errors.Join(context.Canceled, errors.New("independent transport failure"))},
		{"succeeded_with_business_error", globalevents.ResultSucceeded, errors.New("business-only send error")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeCronRepo()
			meta := Metadata{Kind: metadataKind, Version: 1, Title: "partial", Schedule: CronSchedule{Mode: ScheduleCron}, Trigger: CronTrigger{Mode: TriggerDirect, Message: "report"}, Target: CronTarget{SourcePlatform: "cli"}}
			job := upsertTestCronJob(t, repo, meta)
			s := NewService(Options{Store: fakeCronStore{cron: repo}, SendTarget: func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
				return delivery.Receipt{PlatformMessageIDs: []string{"received"}}, globalevents.LogResult{ResultStatus: tc.status, Content: "source delivery diagnostic"}, tc.err
			}})
			var records []globalevents.LogRecord
			var runtimeRecords []globalevents.LogRecord
			connection, err := globalevents.LogSubmitted.Observe(func(_ context.Context, record globalevents.LogRecord) error {
				if record.Category == globalevents.LogRuntime {
					runtimeRecords = append(runtimeRecords, record)
				}
				if record.EventName == "cron.trigger_failed" || record.EventName == "cron_trigger_failed" || record.EventName == "cron.trigger_completed" || record.EventName == "cron_trigger_completed" {
					records = append(records, record)
				}
				return nil
			}, signal.ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Disconnect()
			logResult, err := s.Handler(t.Context(), *job)
			if !errors.Is(err, tc.err) || logResult.ResultStatus != tc.status || len(records) != 1 || len(runtimeRecords) != 0 {
				t.Fatalf("source/business result changed: log=%#v error=%v records=%#v", logResult, err, records)
			}
			for _, record := range records {
				if record.ResultStatus != tc.status || !strings.Contains(record.Content, "source delivery diagnostic") || (record.Level == slog.LevelError) != (tc.status == globalevents.ResultFailed) {
					t.Fatalf("trigger ignored source outcome: %#v", record)
				}
			}
		})
	}
}

func (r *loggingCronRepo) UpdateRunState(ctx context.Context, id string, state storage.CronJobRunState) error {
	r.saved = true
	if r.saveErr != nil {
		return r.saveErr
	}
	return r.fakeCronRepo.UpdateRunState(ctx, id, state)
}

func TestRunJobLogsFinalResultAfterStateSave(t *testing.T) {
	for _, tc := range []struct {
		name         string
		cancel       bool
		runErr       error
		saveErr      error
		sourceResult globalevents.ResultStatus
		want         globalevents.ResultStatus
	}{
		{"success", false, nil, nil, globalevents.ResultSucceeded, globalevents.ResultSucceeded},
		{"canceled", true, fmt.Errorf("wrapped: %w", context.Canceled), nil, globalevents.ResultCanceled, globalevents.ResultCanceled},
		{"deadline", false, context.DeadlineExceeded, nil, globalevents.ResultFailed, globalevents.ResultFailed},
		{"cancel_and_save_failure", true, context.Canceled, errors.New("save failed"), globalevents.ResultCanceled, globalevents.ResultFailed},
		{"success_and_save_failure", false, nil, errors.New("save failed"), globalevents.ResultSucceeded, globalevents.ResultFailed},
		{"mixed_failure", true, errors.Join(context.Canceled, errors.New("independent failure")), nil, globalevents.ResultFailed, globalevents.ResultFailed},
		{"source_rejected_without_error", false, nil, nil, globalevents.ResultRejected, globalevents.ResultRejected},
		{"source_failed_without_error", false, nil, nil, globalevents.ResultFailed, globalevents.ResultFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &loggingCronRepo{fakeCronRepo: newFakeCronRepo(), saveErr: tc.saveErr}
			manager := NewManager(repo)
			ctx, cancel := context.WithCancel(contextinfo.WithExecution(context.Background(), contextinfo.Execution{SessionID: "session", RequestID: "request"}))
			defer cancel()
			manager.started, manager.runCtx = true, ctx
			manager.handlers["handler"] = func(context.Context, storage.CronJob) (globalevents.LogResult, error) {
				if tc.cancel {
					cancel()
				}
				content := "source diagnostic"
				if tc.runErr != nil {
					content += "\n" + tc.runErr.Error()
				}
				return globalevents.LogResult{ResultStatus: tc.sourceResult, Content: content}, tc.runErr
			}
			job, err := repo.Upsert(ctx, storage.UpsertCronJobRequest{Name: "job", Handler: "handler", Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			var final []globalevents.LogRecord
			connection, err := globalevents.LogSubmitted.Observe(func(_ context.Context, record globalevents.LogRecord) error {
				if record.EventName == "cron_job_completed.handler" || record.EventName == "cron_job_failed.handler" {
					if !repo.saved {
						t.Error("final result emitted before state save")
					}
					final = append(final, record)
				}
				return nil
			}, signal.ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Disconnect()
			manager.runJob(job.Name)
			if len(final) != 1 {
				t.Fatalf("final records = %#v", final)
			}
			record := final[0]
			if record.ResultStatus != tc.want {
				t.Fatalf("result = %s, want %s", record.ResultStatus, tc.want)
			}
			if (record.Level == slog.LevelError) != (tc.want == globalevents.ResultFailed) {
				t.Fatalf("level = %s", record.Level)
			}
			if record.Identity.SessionID == nil || *record.Identity.SessionID != "session" || record.Identity.RequestID == nil || *record.Identity.RequestID != "request" {
				t.Fatalf("identity = %#v", record.Identity)
			}
			for _, err := range []error{tc.runErr, tc.saveErr} {
				if err != nil && !strings.Contains(record.Content, err.Error()) {
					t.Fatalf("missing error %v in %q", err, record.Content)
				}
			}
		})
	}
}

func TestRunJobPublishesHandlerContentOnlyOnce(t *testing.T) {
	repo := &loggingCronRepo{fakeCronRepo: newFakeCronRepo()}
	meta := Metadata{Kind: metadataKind, Version: 1, Title: "reminder", Schedule: CronSchedule{Mode: ScheduleCron}, Trigger: CronTrigger{Mode: TriggerDirect, Message: "hello"}, Target: CronTarget{SourcePlatform: "cli"}}
	job := upsertTestCronJob(t, repo.fakeCronRepo, meta)
	s := NewService(Options{Store: fakeCronStore{cron: repo}, SendTarget: func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
		return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: "sent_once\n\nreceipt=one"}, nil
	}})
	manager := NewManager(repo)
	manager.started, manager.runCtx = true, t.Context()
	manager.handlers[job.Handler] = s.Handler
	var records []globalevents.LogRecord
	connection, err := globalevents.LogSubmitted.Observe(func(_ context.Context, record globalevents.LogRecord) error {
		records = append(records, record)
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(connection.Disconnect)
	manager.runJob(job.Name)
	var runtimeCount, auditCount int
	var final globalevents.LogRecord
	for _, record := range records {
		if record.Category == globalevents.LogRuntime {
			runtimeCount++
			if record.EventName == "cron_job_completed."+job.Handler {
				final = record
			}
		} else if record.Category == globalevents.LogAudit {
			auditCount++
		}
	}
	if runtimeCount != 2 || auditCount != 4 || !repo.saved {
		t.Fatalf("runtime=%d audit=%d saved=%v records=%#v", runtimeCount, auditCount, repo.saved, records)
	}
	if final.ResultStatus != globalevents.ResultSucceeded || strings.Count(final.Content, "job=") != 1 || strings.Count(final.Content, "handler=") != 1 || strings.Count(final.Content, "sent_once") != 1 || strings.Contains(final.Content, "\n\n") {
		t.Fatalf("duplicate or incomplete final content: %#v", final)
	}
	if !strings.Contains(final.Content, "platform=cli") || !strings.Contains(final.Content, "kind=text result=succeeded") || !strings.HasPrefix(final.Content, "cron job finished\njob=") {
		t.Fatalf("missing task metadata or target details: %q", final.Content)
	}
}

func TestRecoveryPublishesReturnedDeliveryDetails(t *testing.T) {
	for _, status := range []globalevents.ResultStatus{globalevents.ResultSucceeded, globalevents.ResultFailed} {
		t.Run(status.String(), func(t *testing.T) {
			repo := newFakeCronRepo()
			meta := Metadata{Kind: metadataKind, Version: 1, Title: "reminder", Schedule: CronSchedule{Mode: ScheduleOnce, RunAt: "2026-01-02 03:04:00"}, Trigger: CronTrigger{Mode: TriggerDirect, Message: "hello"}, Target: CronTarget{SourcePlatform: "cli"}}
			job := upsertTestCronJob(t, repo, meta)
			s := NewService(Options{Store: fakeCronStore{cron: repo}, SendTarget: func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
				return delivery.Receipt{}, globalevents.LogResult{ResultStatus: status, Content: "delivery_detail\n\nreceipt=one"}, nil
			}})
			s.now = func() time.Time { return mustParseTestTime(t, "2026-01-02 03:05:00") }
			var final []globalevents.LogRecord
			connection, err := globalevents.LogSubmitted.Observe(func(_ context.Context, record globalevents.LogRecord) error {
				if record.EventName == "cron_missed_delivery_completed" || record.EventName == "missed_cron_run_failed" {
					final = append(final, record)
				}
				return nil
			}, signal.ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(connection.Disconnect)
			s.runMissedOnceForPlatform(t.Context(), "cli")
			if len(final) != 1 || final[0].ResultStatus != status || (final[0].Level == slog.LevelError) != (status == globalevents.ResultFailed) {
				t.Fatalf("recovery outcome = %#v", final)
			}
			if strings.Count(final[0].Content, "delivery_detail") != 1 || strings.Contains(final[0].Content, "\n\n") || !strings.Contains(final[0].Content, "kind=text result="+status.String()) {
				t.Fatalf("recovery lost delivery body: %q", final[0].Content)
			}
			for _, value := range []string{"recovery_platform=cli", "source_platform=cli", "target_platforms=cli", "target_all_enabled_platforms=false"} {
				if !strings.Contains(final[0].Content, value) {
					t.Fatalf("recovery lost %q: %q", value, final[0].Content)
				}
			}
			if repo.jobs[job.Name].Enabled {
				t.Fatal("completed once task was not disabled")
			}
		})
	}
}
