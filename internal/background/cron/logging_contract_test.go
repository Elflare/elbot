package cron

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
	"elbot/internal/operationresult"
	"elbot/internal/signal"
)

type loggingCronRepo struct {
	*fakeCronRepo
	saveErr error
	saved   bool
}

func TestDeliverySuccessHasIndependentAudit(t *testing.T) {
	ctx := context.Background()
	repo := newFakeCronRepo()
	meta := Metadata{Kind: metadataKind, Version: 1, Title: "partial", Schedule: CronSchedule{Mode: ScheduleCron}, Trigger: CronTrigger{Mode: TriggerDirect, Message: "report"}, Target: CronTarget{SourcePlatform: "cli"}}
	job := upsertTestCronJob(t, repo, meta)
	s := newTestCronService(t, Options{Store: repo, SendTarget: func(context.Context, delivery.Target, []delivery.Output) (receiptResult, error) {
		return sentReceipt(delivery.Receipt{PlatformMessageIDs: []string{"received"}}), nil
	}})
	var records []globalevents.LogRecord
	connection, err := globalevents.LogSubmitted.Observe(func(_ context.Context, record globalevents.LogRecord) {
		if record.EventName == "cron.send_failed" || record.EventName == "cron.send_completed" {
			records = append(records, record)
		}

	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(connection.Disconnect)
	_, result, err := s.runTask(ctx, *job, meta)
	if err != nil || result.Status != operationresult.Success {
		t.Fatal("delivery result:", result, "error:", err)
	}
	if len(records) != 1 || records[0].ResultStatus != globalevents.ResultSucceeded || records[0].Level != slog.LevelInfo {
		t.Fatalf("source failure ignored: %#v", records)
	}
}

func TestHandlerPreservesDeliveryBusinessResultAndFault(t *testing.T) {
	for _, tc := range []struct {
		name        string
		delivery    receiptResult
		deliveryErr error
		wantStatus  operationresult.Status
	}{
		{name: "success", delivery: sentReceipt(delivery.Receipt{}), wantStatus: operationresult.Success},
		{name: "rejected", delivery: receiptResult{Status: operationresult.Rejected, Message: "target rejected"}, wantStatus: operationresult.Rejected},
		{name: "fault", deliveryErr: errors.New("transport failure")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeCronRepo()
			meta := Metadata{Kind: metadataKind, Version: 1, Title: "partial", Schedule: CronSchedule{Mode: ScheduleCron}, Trigger: CronTrigger{Mode: TriggerDirect, Message: "report"}, Target: CronTarget{SourcePlatform: "cli"}}
			job := upsertTestCronJob(t, repo, meta)
			s := newTestCronService(t, Options{Store: repo, SendTarget: func(context.Context, delivery.Target, []delivery.Output) (receiptResult, error) {
				return tc.delivery, tc.deliveryErr
			}})
			_, result, err := s.Handler(t.Context(), *job)
			if tc.deliveryErr != nil {
				if !errors.Is(err, tc.deliveryErr) || result.Status == operationresult.Success {
					t.Fatalf("result=%+v error=%v", result, err)
				}
				return
			}
			if err != nil || result.Status != tc.wantStatus {
				t.Fatalf("result=%+v error=%v, want status %s", result, err, tc.wantStatus)
			}
		})
	}
}

func TestTaskAuditIdentityAndOperation(t *testing.T) {
	for _, mode := range []TriggerMode{TriggerDirect, TriggerLLM} {
		for _, withIdentity := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/identity=%t", mode, withIdentity), func(t *testing.T) {
				ctx := t.Context()
				want := globalevents.LogIdentity{SessionID: "", RunID: "", Attempt: "", RequestID: "", RootRequestID: ""}
				if withIdentity {
					facts := contextinfo.Execution{SessionID: "source-session", RunID: "run", Attempt: "attempt", RequestID: "request", RootRequestID: "root"}
					ctx = contextinfo.WithExecution(ctx, facts)
					want = globalevents.LogIdentity{SessionID: facts.SessionID, RunID: facts.RunID, Attempt: facts.Attempt, RequestID: facts.RequestID, RootRequestID: facts.RootRequestID}
				}
				repo := newFakeCronRepo()
				job := upsertTestCronJob(t, repo, Metadata{Kind: metadataKind, Schedule: CronSchedule{Mode: ScheduleCron}, Trigger: CronTrigger{Mode: mode, Message: "report"}, Target: CronTarget{SourcePlatform: "cli"}})
				s := newTestCronService(t, Options{Store: repo, Runner: &fakeCronRunner{text: `{"completed":true,"need_report":true,"report":"report"}`}, SendTarget: func(context.Context, delivery.Target, []delivery.Output) (receiptResult, error) {
					return sentReceipt(delivery.Receipt{}), nil
				}})
				var records []globalevents.LogRecord
				connection, err := globalevents.LogSubmitted.Observe(func(_ context.Context, record globalevents.LogRecord) {
					if strings.HasPrefix(record.EventName, "cron.trigger_") || strings.HasPrefix(record.EventName, "cron.send_") {
						records = append(records, record)
					}

				}, signal.ConnectOptions{})
				if err != nil {
					t.Fatal(err)
				}
				defer connection.Disconnect()
				_, result, err := s.Handler(ctx, *job)
				if err != nil || result.Status != operationresult.Success || len(records) != 3 {
					t.Fatal("result= err= records=", result, err, records)
				}
				for i, name := range []string{"cron.trigger_started", "cron.send_started", "cron.send_completed"} {
					identity := want
					if strings.HasPrefix(name, "cron.send_") {
						sessionID := ""
						if mode == TriggerLLM {
							sessionID = "cron-session"
						}
						identity.SessionID = sessionID
					}
					if records[i].EventName != name || !reflect.DeepEqual(records[i].Identity, identity) {
						t.Fatalf("record=%+v want event=%s identity=%+v", records[i], name, identity)
					}
				}
			})
		}
	}
}

func (r *loggingCronRepo) UpdateRunState(ctx context.Context, id string, state JobRunState) error {
	r.saved = true
	if r.saveErr != nil {
		return r.saveErr
	}
	return r.fakeCronRepo.UpdateRunState(ctx, id, state)
}

func TestRunJobReportsTaskAndBookkeepingFailuresIndependently(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cancel  bool
		runErr  error
		saveErr error
		want    globalevents.ResultStatus
	}{
		{"success", false, nil, nil, globalevents.ResultSucceeded},
		{"canceled", true, fmt.Errorf("wrapped: %w", context.Canceled), nil, globalevents.ResultCanceled},
		{"deadline", false, context.DeadlineExceeded, nil, globalevents.ResultFailed},
		{"cancel_and_save_failure", true, context.Canceled, errors.New("save failed"), globalevents.ResultCanceled},
		{"success_and_save_failure", false, nil, errors.New("save failed"), globalevents.ResultSucceeded},
		{"mixed_failure", true, errors.New("independent failure"), nil, globalevents.ResultFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &loggingCronRepo{fakeCronRepo: newFakeCronRepo(), saveErr: tc.saveErr}
			manager := NewManager(repo)
			ctx, cancel := context.WithCancel(contextinfo.WithExecution(context.Background(), contextinfo.Execution{SessionID: "session", RequestID: "request"}))
			defer cancel()
			manager.started, manager.runCtx = true, ctx
			manager.handlers["handler"] = func(ctx context.Context, _ Job) (context.Context, jobResult, error) {
				if tc.cancel {
					cancel()
				}
				if tc.runErr != nil {
					return ctx, jobResult{}, tc.runErr
				}
				return successfulJob(ctx)
			}
			job, err := repo.Upsert(ctx, UpsertJobRequest{Name: "job", Handler: "handler", Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			var final []globalevents.LogRecord
			var saves []globalevents.LogRecord
			connection, err := globalevents.LogSubmitted.Observe(func(_ context.Context, record globalevents.LogRecord) {
				if record.EventName == "cron_job_completed.handler" || record.EventName == "cron_job_failed.handler" {
					if repo.saved {
						t.Error("task failure was retained through bookkeeping")
					}
					final = append(final, record)
				}
				if record.EventName == "cron_job_state_update_failed" {
					saves = append(saves, record)
				}

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
			wantLevel := slog.LevelInfo
			if tc.want == globalevents.ResultFailed {
				wantLevel = slog.LevelWarn
			}
			if record.Level != wantLevel {
				t.Fatalf("level = %s, want %s", record.Level, wantLevel)
			}
			if record.Identity.SessionID != "session" || record.Identity.RequestID != "request" {
				t.Fatalf("identity = %#v", record.Identity)
			}
			if tc.runErr != nil && !strings.Contains(record.Content, tc.runErr.Error()) {
				t.Fatalf("task error missing: %+v", record)
			}
			if tc.saveErr != nil {
				if len(saves) != 1 || !strings.Contains(saves[0].Content, tc.saveErr.Error()) || strings.Contains(record.Content, tc.saveErr.Error()) {
					t.Fatalf("bookkeeping error mixed with task result: final=%+v saves=%+v", final, saves)
				}
			} else if len(saves) != 0 {
				t.Fatalf("unexpected save failure: %+v", saves)
			}
		})
	}
}

func TestRunJobPublishesHandlerContentOnlyOnce(t *testing.T) {
	repo := &loggingCronRepo{fakeCronRepo: newFakeCronRepo()}
	meta := Metadata{Kind: metadataKind, Version: 1, Title: "reminder", Schedule: CronSchedule{Mode: ScheduleCron}, Trigger: CronTrigger{Mode: TriggerDirect, Message: "hello"}, Target: CronTarget{SourcePlatform: "cli"}}
	job := upsertTestCronJob(t, repo.fakeCronRepo, meta)
	s := newTestCronService(t, Options{Store: repo, SendTarget: func(context.Context, delivery.Target, []delivery.Output) (receiptResult, error) {
		return sentReceipt(delivery.Receipt{}), nil
	}})
	manager := NewManager(repo)
	manager.started, manager.runCtx = true, t.Context()
	manager.handlers[job.Handler] = s.Handler
	var records []globalevents.LogRecord
	connection, err := globalevents.LogSubmitted.Observe(func(_ context.Context, record globalevents.LogRecord) {
		records = append(records, record)

	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(connection.Disconnect)
	manager.runJob(job.Name)
	var runtimeCount, auditCount int
	var final globalevents.LogRecord
	for _, record := range records {
		if record.Category == globalevents.LogCategoryLog {
			runtimeCount++
			if record.EventName == "cron_job_completed."+job.Handler {
				final = record
			}
		} else if record.Category == globalevents.LogCategoryAudit {
			auditCount++
		}
	}
	if runtimeCount != 2 || auditCount != 3 || !repo.saved {
		t.Fatalf("runtime=%d audit=%d saved=%v records=%#v", runtimeCount, auditCount, repo.saved, records)
	}
	if final.ResultStatus != globalevents.ResultSucceeded || strings.Count(final.Content, "job=") != 1 || strings.Count(final.Content, "handler=") != 1 || strings.Contains(final.Content, "\n\n") {
		t.Fatalf("duplicate or incomplete final content: %#v", final)
	}
	if !strings.HasPrefix(final.Content, "cron job finished\njob=") {
		t.Fatalf("missing task metadata or target details: %q", final.Content)
	}
}

func TestRecoveryPublishesReturnedDeliveryDetails(t *testing.T) {
	for _, status := range []globalevents.ResultStatus{globalevents.ResultFailed} {
		t.Run(status.String(), func(t *testing.T) {
			repo := newFakeCronRepo()
			meta := Metadata{Kind: metadataKind, Version: 1, Title: "reminder", Schedule: CronSchedule{Mode: ScheduleOnce, RunAt: "2026-01-02 03:04:00"}, Trigger: CronTrigger{Mode: TriggerDirect, Message: "hello"}, Target: CronTarget{SourcePlatform: "cli"}}
			job := upsertTestCronJob(t, repo, meta)
			s := newTestCronService(t, Options{Store: repo, SendTarget: func(context.Context, delivery.Target, []delivery.Output) (receiptResult, error) {
				if status == globalevents.ResultFailed {
					return receiptResult{}, errors.New("delivery failed")
				}
				return sentReceipt(delivery.Receipt{}), nil
			}})
			s.now = func() time.Time { return mustParseTestTime(t, "2026-01-02 03:05:00") }
			var final []globalevents.LogRecord
			connection, err := globalevents.LogSubmitted.Observe(func(_ context.Context, record globalevents.LogRecord) {
				if record.EventName == "cron_missed_delivery_completed" || record.EventName == "missed_cron_run_failed" {
					final = append(final, record)
				}

			}, signal.ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(connection.Disconnect)
			s.runMissedOnceForPlatform(t.Context(), "cli")
			if len(final) != 1 || final[0].ResultStatus != status || (final[0].Level == slog.LevelWarn) != (status == globalevents.ResultFailed) {
				t.Fatalf("recovery outcome = %#v", final)
			}

			for _, value := range []string{"recovery_platform=cli", "source_platform=cli", "target_platforms=cli", "target_all_enabled_platforms=false"} {
				if !strings.Contains(final[0].Content, value) {
					t.Fatalf("recovery lost %q: %q", value, final[0].Content)
				}
			}
			if repo.jobs[job.Name].Enabled != (status == globalevents.ResultFailed) {
				t.Fatal("completed once task was not disabled")
			}
		})
	}
}
