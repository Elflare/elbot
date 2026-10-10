package cron

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
	"elbot/internal/signal"
)

type gateWaitContext struct {
	context.Context
	started chan struct{}
	once    sync.Once
}

func (c *gateWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.started) })
	return c.Context.Done()
}

func TestDeliveryGateWaitCanBeCancelled(t *testing.T) {
	s := NewService(Options{})
	unlock, err := s.lockDeliveryJob(context.Background(), "same")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	waitingCtx := &gateWaitContext{Context: ctx, started: started}
	done := make(chan error, 1)
	go func() {
		release, err := s.lockDeliveryJob(waitingCtx, "same")
		if release != nil {
			release()
		}
		done <- err
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("gate ignored cancellation")
	}
	unlock()
	unlock, err = s.lockDeliveryJob(context.Background(), "same")
	if err != nil {
		t.Fatal(err)
	}
	unlock()
}

func TestRecoveryCancellationDoesNotSendFailureNotice(t *testing.T) {
	repo := newFakeCronRepo()
	var logs bytes.Buffer
	var targets []string
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	captureLogs(t, slog.New(slog.NewTextHandler(&logs, nil)))
	s := NewService(Options{Store: fakeCronStore{cron: repo}, EnabledPlatforms: []PlatformTarget{{Name: "qqonebot"}}, SendTarget: func(_ context.Context, target delivery.Target, _ []delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
		targets = append(targets, target.Platform)
		cancel()
		{
			err := context.Canceled
			logResult := globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}
			if err != nil {
				logResult.ResultStatus = globalevents.ResultFailed
				logResult.Content = err.Error()
				if err == context.Canceled {
					logResult.ResultStatus = globalevents.ResultCanceled
				}
			}
			return delivery.Receipt{}, logResult, err
		}
	}})
	s.now = func() time.Time { return mustParseTestTime(t, "2026-01-02 03:05:00") }
	upsertTestCronJob(t, repo, Metadata{Kind: metadataKind, Version: 1, Title: "cancel", Schedule: CronSchedule{Mode: ScheduleOnce, RunAt: "2026-01-02 03:04:00"}, Trigger: CronTrigger{Mode: TriggerDirect, Message: "report"}, Target: CronTarget{AllEnabledPlatforms: true, SourcePlatform: "cli"}})
	s.handlePlatformConnected(ctx, "qqonebot")
	if len(targets) != 1 || targets[0] != "qqonebot" {
		t.Fatalf("unexpected failure notification: %v", targets)
	}
	if strings.Contains(logs.String(), "level=WARN") || strings.Contains(logs.String(), "level=ERROR") {
		t.Fatalf("cancel logged as failure: %s", logs.String())
	}
}

func TestDeliveryKeepsMixedFailureBeforeErrorWrapping(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	failure := errors.New("real failure")
	s := NewService(Options{SendTarget: func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
		cancel()
		{
			err := fmt.Errorf("mixed: %w", errors.Join(context.Canceled, failure))
			logResult := globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}
			if err != nil {
				logResult.ResultStatus = globalevents.ResultFailed
				logResult.Content = err.Error()
				if err == context.Canceled {
					logResult.ResultStatus = globalevents.ResultCanceled
				}
			}
			return delivery.Receipt{}, logResult, err
		}
	}})
	logResult, err := s.sendOutputsToPlatformTarget(ctx, "mixed", "cli", delivery.Target{Platform: "cli"}, []delivery.Output{delivery.Text("report")}, "", "")
	status := logResult.ResultStatus
	if status != globalevents.ResultFailed || !errors.Is(err, failure) || !errors.Is(err, context.Canceled) {
		t.Fatalf("status=%s error=%v", status, err)
	}
}

// captureLogs observes the public signal; production never receives this logger.
func captureLogs(t *testing.T, logger *slog.Logger) {
	t.Helper()
	connection, err := globalevents.LogSubmitted.Connect(func(ctx context.Context, record globalevents.LogRecord) error {
		if !logger.Enabled(ctx, record.Level) {
			return nil
		}
		out := slog.NewRecord(time.Now(), record.Level, record.Content, 0)
		out.AddAttrs(slog.String("event", record.EventName), slog.String("module", record.Module), slog.String("result_status", record.ResultStatus.String()))
		return logger.Handler().Handle(ctx, out)
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(connection.Disconnect)
}
