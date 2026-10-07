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
	s := NewService(Options{Store: fakeCronStore{cron: repo}, EnabledPlatforms: []PlatformTarget{{Name: "qqonebot"}}, SendTarget: func(_ context.Context, target delivery.Target, _ []delivery.Output) (delivery.Receipt, error) {
		targets = append(targets, target.Platform)
		cancel()
		return delivery.Receipt{}, context.Canceled
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
	if isContextCancellation(ctx, fmt.Errorf("mixed: %w", errors.Join(context.Canceled, errors.New("real failure")))) {
		t.Fatal("mixed error swallowed")
	}
}

// captureLogs observes the public signal; production never receives this logger.
func captureLogs(t *testing.T, logger *slog.Logger) {
	t.Helper()
	connection, err := globalevents.LogSubmitted.Connect(func(ctx context.Context, record globalevents.LogRecord) error {
		if !logger.Enabled(ctx, record.Level) {
			return nil
		}
		out := slog.NewRecord(record.At, record.Level, record.Summary, 0)
		out.AddAttrs(record.Fields...)
		out.AddAttrs(slog.String("event", record.Name), slog.String("module", record.Module))
		if record.Detail != "" && logger.Enabled(ctx, slog.LevelDebug) {
			out.AddAttrs(slog.String("detail", record.Detail))
		}
		return logger.Handler().Handle(ctx, out)
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(connection.Disconnect)
}
