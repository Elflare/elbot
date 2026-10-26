package maintenance

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"elbot/internal/background/share"
	"elbot/internal/config"
	"elbot/internal/events"
)

func TestCleanupFunctionsPreserveResults(t *testing.T) {
	for _, kind := range []string{"session", "history"} {
		for _, tc := range []struct {
			name    string
			enabled bool
			err     error
			status  events.ResultStatus
		}{
			{"success", true, nil, events.ResultSucceeded},
			{"partial failure", true, errors.New("partial cleanup"), events.ResultFailed},
			{"canceled", true, context.Canceled, events.ResultCanceled},
			{"deadline", true, context.DeadlineExceeded, events.ResultFailed},
			{"disabled", false, nil, events.ResultSkipped},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				before := time.Now().AddDate(0, 0, -7)
				var cutoff time.Time
				calls := 0
				cleanup := func(got context.Context, date time.Time) (int, error) {
					if got != ctx {
						t.Fatal("context was replaced")
					}
					calls++
					cutoff = date
					return 3, tc.err
				}
				cfg := &config.Config{Maintenance: config.MaintenanceConfig{
					SessionCleanup:     config.MaintenanceCleanupConfig{Enabled: tc.enabled, RetentionDays: 7},
					ChatHistoryCleanup: config.ChatHistoryCleanupConfig{Enabled: tc.enabled, RetentionDays: 7},
				}}
				// Media 未注入：历史清理不再自行触发媒体清理或引用对账。
				svc := NewServiceWithConfig(nil, cleanup, cfg, cleanup)
				run := svc.RunSessionCleanup
				if kind == "history" {
					run = svc.RunChatHistoryCleanup
				}
				result, err := run(ctx)
				if !errors.Is(err, tc.err) || result.ResultStatus != tc.status {
					t.Fatalf("result=%#v err=%v", result, err)
				}
				if !tc.enabled {
					if calls != 0 {
						t.Fatal("disabled cleanup called dependency")
					}
					return
				}
				if calls != 1 || cutoff.Before(before) || cutoff.After(time.Now().AddDate(0, 0, -7)) {
					t.Fatalf("calls=%d cutoff=%v", calls, cutoff)
				}
				if !strings.Contains(result.Content, "deleted=3\nretention_days=7") {
					t.Fatalf("missing partial count: %q", result.Content)
				}
			})
		}
	}
}

type cleanupMediaFunc func(context.Context) (events.LogResult, error)

func (f cleanupMediaFunc) Cleanup(ctx context.Context) (events.LogResult, error) { return f(ctx) }

func TestMaintenanceRegistrationAndFailure(t *testing.T) {
	want := []string{"maintenance.log_cleanup", "maintenance.session_cleanup", "maintenance.sandbox_cleanup", "maintenance.media_cleanup", "maintenance.chat_history_cleanup"}
	boom := errors.New("registration failed")
	for failAt := 0; failAt <= len(want); failAt++ {
		svc := &Service{Media: cleanupMediaFunc(func(context.Context) (events.LogResult, error) {
			return events.LogResult{ResultStatus: events.ResultFailed, Content: "media partial result"}, boom
		})}
		var names []string
		err := svc.RegisterCronHandlers(func(name string, handler func(context.Context) (events.LogResult, error)) error {
			names = append(names, name)
			if name == "maintenance.media_cleanup" {
				result, err := handler(context.Background())
				if result.ResultStatus != events.ResultFailed || result.Content != "media partial result" || err != boom {
					t.Fatalf("media result=%#v err=%v", result, err)
				}
			}
			if len(names) == failAt {
				return boom
			}
			return nil
		})
		count := len(want)
		var expected error
		if failAt > 0 {
			count, expected = failAt, boom
		}
		if err != expected || !reflect.DeepEqual(names, want[:count]) {
			t.Fatalf("failAt=%d names=%v err=%v", failAt, names, err)
		}
	}
}

func TestSetupCronConfigurationAndFailure(t *testing.T) {
	cfg := &config.Config{Maintenance: config.MaintenanceConfig{
		LogCleanup:         config.CronTaskConfig{Enabled: true, Schedule: "0 1 * * *"},
		SessionCleanup:     config.MaintenanceCleanupConfig{Enabled: false, Schedule: "0 2 * * *"},
		SandboxCleanup:     config.MaintenanceCleanupConfig{Enabled: false, Schedule: "0 3 * * *"},
		ChatHistoryCleanup: config.ChatHistoryCleanupConfig{Enabled: true, Schedule: "0 4 * * *"},
	}}
	want := []share.MaintenanceJob{
		{Name: "system.maintenance.log_cleanup", Handler: "maintenance.log_cleanup", Schedule: "0 1 * * *", Enabled: true},
		{Name: "system.maintenance.session_cleanup", Handler: "maintenance.session_cleanup", Schedule: "0 2 * * *"},
		{Name: "system.maintenance.sandbox_cleanup", Handler: "maintenance.sandbox_cleanup", Schedule: "0 3 * * *"},
		{Name: "system.maintenance.chat_history_cleanup", Handler: "maintenance.chat_history_cleanup", Schedule: "0 4 * * *", Enabled: true},
		{Name: "system.maintenance.media_cleanup", Handler: "maintenance.media_cleanup", Schedule: "0 3 * * *", Enabled: true},
	}
	ctx := context.Background()
	boom := errors.New("configuration failed")
	for failAt := 0; failAt <= len(want); failAt++ {
		var jobs []share.MaintenanceJob
		err := SetupCron(ctx, func(got context.Context, job share.MaintenanceJob) error {
			if got != ctx {
				t.Fatal("context was replaced")
			}
			jobs = append(jobs, job)
			if len(jobs) == failAt {
				return boom
			}
			return nil
		}, cfg)
		count := len(want)
		var expected error
		if failAt > 0 {
			count, expected = failAt, boom
		}
		if err != expected || !reflect.DeepEqual(jobs, want[:count]) {
			t.Fatalf("failAt=%d jobs=%#v err=%v", failAt, jobs, err)
		}
	}
}
