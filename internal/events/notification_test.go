package events

import (
	"context"
	"os"
	"strings"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/signal"
)

func captureReports(t *testing.T, receiveNotifications bool) (chan LogRecord, chan Notification, func()) {
	t.Helper()
	previousLogs, previousNotices := LogSubmitted, NotificationRequested
	LogSubmitted = signal.New[LogRecord, signal.Empty]("test.logs", signal.Options[LogRecord]{Concurrency: 1})
	NotificationRequested = signal.New[Notification, signal.Empty]("test.notifications", signal.Options[Notification]{Required: true, Single: true})
	logs, notices := make(chan LogRecord, 8), make(chan Notification, 8)
	if _, err := LogSubmitted.Observe(func(_ context.Context, record LogRecord) {
		logs <- record

	}, signal.ConnectOptions{Shutdown: signal.Drain}); err != nil {
		t.Fatal(err)
	}
	if receiveNotifications {
		if _, err := NotificationRequested.Observe(func(_ context.Context, n Notification) {
			notices <- n

		}, signal.ConnectOptions{Shutdown: signal.Drain}); err != nil {
			t.Fatal(err)
		}
	}
	drain := func() {
		t.Helper()
		if err := NotificationRequested.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := LogSubmitted.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		drain()
		LogSubmitted, NotificationRequested = previousLogs, previousNotices
	})
	return logs, notices, drain
}

func TestReportSelectionPreservesSourceFacts(t *testing.T) {
	for _, mode := range []string{"log", "notify", "both"} {
		t.Run(mode, func(t *testing.T) {
			logs, notices, drain := captureReports(t, true)
			id := "source-request"
			record := LogRecord{Category: LogRuntime, Content: "provider: original detail", Identity: LogIdentity{RequestID: id}}
			notice := Notification{Text: record.Content, Target: NotificationTarget{Platform: "cli", ID: "recipient"}}
			ctx := contextinfo.WithExecution(t.Context(), contextinfo.Execution{RequestID: "other-request"})
			switch mode {
			case "log":
				AsyncLog(ctx, record)
			case "notify":
				Notify(ctx, notice)
			case "both":
				LogAndNotify(ctx, record, notice)
			}
			id = "changed-after-publication"
			drain()
			if mode != "notify" {
				if len(logs) != 1 {
					t.Fatalf("logs = %d", len(logs))
				}
				got := <-logs
				if got.Content != record.Content || got.Identity.RequestID != "source-request" {
					t.Fatalf("source facts changed: %+v", got)
				}
			}
			if mode != "log" {
				if len(notices) != 1 || <-notices != notice {
					t.Fatal("notification changed or missing")
				}
			}
			if len(logs) != 0 || len(notices) != 0 {
				t.Fatal("unexpected additional report")
			}
		})
	}
}

func TestLogFailureDoesNotPreventNotification(t *testing.T) {
	_, notices, drain := captureReports(t, true)
	if err := LogSubmitted.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	LogAndNotify(t.Context(), LogRecord{Category: LogRuntime}, Notification{Text: "still deliver"})
	drain()
	if len(notices) != 1 || (<-notices).Text != "still deliver" {
		t.Fatal("log rejection prevented notification")
	}
}

func TestNotificationAdmissionFailureIsLoggedOnce(t *testing.T) {
	if err := signal.FlushDiagnostics(t.Context()); err != nil {
		t.Fatal(err)
	}
	file, err := os.CreateTemp(t.TempDir(), "framework-diagnostic")
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stderr
	os.Stderr = file
	defer func() { _ = signal.FlushDiagnostics(context.Background()); os.Stderr = previous; _ = file.Close() }()
	logs, notices, drain := captureReports(t, false)
	ctx := contextinfo.WithExecution(t.Context(), contextinfo.Execution{RequestID: "request"})
	Notify(ctx, Notification{Text: "original notification"})
	drain()
	if len(logs) != 0 || len(notices) != 0 {
		t.Fatalf("logs=%d notices=%d", len(logs), len(notices))
	}
	if err := signal.FlushDiagnostics(t.Context()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file.Name())
	if err != nil || strings.Count(string(data), signal.ErrNoReceiver.Error()) != 1 {
		t.Fatalf("framework diagnostic: %s error=%v", data, err)
	}
}

func TestReportCancellationKeepsLogAndSuppressesNotice(t *testing.T) {
	logs, notices, drain := captureReports(t, true)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	LogAndNotify(ctx, LogRecord{Category: LogRuntime, Content: "canceled", ResultStatus: ResultCanceled}, Notification{Text: "stale"})
	drain()
	if len(logs) != 1 || len(notices) != 0 || (<-logs).ResultStatus != ResultCanceled {
		t.Fatal("cancellation lost its log or generated an extra notification failure")
	}
}
