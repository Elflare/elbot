package cron

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"elbot/internal/contextinfo"
	globalevents "elbot/internal/events"
	"elbot/internal/signal"
	"elbot/internal/storage"
)

type loggingCronRepo struct {
	*fakeCronRepo
	saveErr error
	saved   bool
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
		name    string
		cancel  bool
		runErr  error
		saveErr error
		want    globalevents.ResultStatus
	}{
		{"success", false, nil, nil, globalevents.ResultSucceeded},
		{"canceled", true, fmt.Errorf("wrapped: %w", context.Canceled), nil, globalevents.ResultCanceled},
		{"deadline", false, context.DeadlineExceeded, nil, globalevents.ResultFailed},
		{"cancel_and_save_failure", true, context.Canceled, errors.New("save failed"), globalevents.ResultFailed},
		{"success_and_save_failure", false, nil, errors.New("save failed"), globalevents.ResultFailed},
		{"mixed_failure", true, errors.Join(context.Canceled, errors.New("independent failure")), nil, globalevents.ResultFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &loggingCronRepo{fakeCronRepo: newFakeCronRepo(), saveErr: tc.saveErr}
			manager := NewManager(repo)
			ctx, cancel := context.WithCancel(contextinfo.WithExecution(context.Background(), contextinfo.Execution{SessionID: "session", RequestID: "request"}))
			defer cancel()
			manager.started, manager.runCtx = true, ctx
			manager.handlers["handler"] = func(context.Context, storage.CronJob) error {
				if tc.cancel {
					cancel()
				}
				return tc.runErr
			}
			job, err := repo.Upsert(ctx, storage.UpsertCronJobRequest{Name: "job", Handler: "handler", Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			var final []globalevents.LogRecord
			connection, err := globalevents.LogSubmitted.Connect(func(_ context.Context, record globalevents.LogRecord) error {
				if record.EventName == "cron_job_completed" || record.EventName == "cron_job_failed" {
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
