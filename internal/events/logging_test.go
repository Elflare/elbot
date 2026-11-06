package events

import (
	"context"
	"log/slog"
	"reflect"
	"testing"
	"testing/synctest"

	"elbot/internal/contextinfo"
	"elbot/internal/signal"
)

func TestLogEnums(t *testing.T) {
	for value := 0; value <= 255; value++ {
		want := map[LogCategory]string{LogRuntime: "runtime", LogAudit: "audit", LogElnis: "elnis"}[LogCategory(value)]
		if LogCategory(value).String() != want {
			t.Fatalf("category %d: string=%q", value, LogCategory(value).String())
		}
		want = map[LogContentType]string{ContentText: "text", ContentJSON: "json"}[LogContentType(value)]
		if LogContentType(value).String() != want {
			t.Fatalf("type %d: string=%q", value, LogContentType(value).String())
		}
		want = map[ResultStatus]string{ResultSucceeded: "succeeded", ResultFailed: "failed", ResultCanceled: "canceled", ResultRejected: "rejected", ResultSkipped: "skipped"}[ResultStatus(value)]
		if ResultStatus(value).String() != want {
			t.Fatalf("result %d: string=%q", value, ResultStatus(value).String())
		}
	}
	if LogRuntime != 1 || LogAudit != 2 || LogElnis != 3 || ContentText != 0 || ContentJSON != 1 || ResultUnspecified != 0 || ResultSucceeded != 1 || ResultSkipped != 5 {
		t.Fatal("enum values changed")
	}
}

func TestLogIdentityFromContextPreservesSnapshot(t *testing.T) {
	source := contextinfo.Execution{SessionID: "session", RunID: "run", Attempt: "attempt", RequestID: "request", RootRequestID: "root"}
	got := LogIdentityFromContext(contextinfo.WithExecution(t.Context(), source))
	source.SessionID = "changed"
	want := LogIdentity{SessionID: "session", RunID: "run", Attempt: "attempt", RequestID: "request", RootRequestID: "root"}
	if got != want {
		t.Fatalf("identity = %+v, want %+v", got, want)
	}
	if got := LogIdentityFromContext(t.Context()); got != (LogIdentity{}) {
		t.Fatalf("empty execution gained identity: %+v", got)
	}
}

func TestLogPublicationModes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		publish func(context.Context, LogRecord)
		async   bool
	}{
		{name: "sync", publish: Log},
		{name: "async", publish: AsyncLog, async: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				previous := LogSubmitted
				LogSubmitted = signal.New[LogRecord, signal.Empty]("test.logs")
				defer func() {
					_ = LogSubmitted.Close(context.Background())
					LogSubmitted = previous
				}()
				started, release := make(chan struct{}), make(chan struct{})
				received := make(chan LogRecord, 1)
				_, err := LogSubmitted.Observe(func(ctx context.Context, record LogRecord) {
					if ctx.Err() != nil {
						t.Error("publication retained cancellation")
					}
					close(started)
					<-release
					received <- record

				}, signal.ConnectOptions{Shutdown: signal.Drain})
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(contextinfo.WithExecution(t.Context(), contextinfo.Execution{SessionID: "context-session"}))
				cancel()
				record := LogRecord{Category: LogRuntime, Content: "original", Identity: LogIdentity{SessionID: "source-session"}}
				done := make(chan struct{})
				go func() {
					tc.publish(ctx, record)
					close(done)
				}()
				<-started
				synctest.Wait()
				returned := false
				select {
				case <-done:
					returned = true
				default:
				}
				if returned != tc.async {
					t.Errorf("returned before consumer finished = %v, want %v", returned, tc.async)
				}
				record.Identity.SessionID = "changed"
				record.Content = "changed"
				close(release)
				<-done
				got := <-received
				if got.Identity.SessionID != "source-session" || got.Content != "original" {
					t.Fatalf("published record changed: %+v", got)
				}
			})
		})
	}
}

func TestLogPreservesSourceRecord(t *testing.T) {
	var received []LogRecord
	connection, err := LogSubmitted.Observe(func(_ context.Context, record LogRecord) {
		received = append(received, record)

	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	ctx := contextinfo.WithExecution(t.Context(), contextinfo.Execution{SessionID: "context-session"})
	for _, record := range []LogRecord{
		{Category: LogRuntime},
		{Category: LogAudit, Level: slog.LevelWarn, ResultStatus: ResultFailed, Content: "reason"},
		{Category: LogElnis, Level: slog.LevelError, ResultStatus: ResultCanceled},
		{Category: LogAudit, Type: ContentJSON, Content: "null"},
		{Category: LogAudit, Type: ContentJSON, Content: "9007199254740993", ContentLimit: 1},
	} {
		Log(ctx, record)
		if !reflect.DeepEqual(received[len(received)-1], record) {
			t.Fatalf("business facts changed: %+v", received)
		}
	}
}
