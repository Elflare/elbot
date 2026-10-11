package events

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/signal"
)

func TestLogEnums(t *testing.T) {
	for value := 0; value <= 255; value++ {
		want := map[LogCategory]string{LogRuntime: "runtime", LogAudit: "audit", LogElnis: "elnis"}[LogCategory(value)]
		if LogCategory(value).Valid() != (want != "") || LogCategory(value).String() != want {
			t.Fatalf("category %d: valid=%v string=%q", value, LogCategory(value).Valid(), LogCategory(value).String())
		}
		want = map[LogContentType]string{ContentText: "text", ContentJSON: "json"}[LogContentType(value)]
		if LogContentType(value).Valid() != (want != "") || LogContentType(value).String() != want {
			t.Fatalf("type %d: valid=%v string=%q", value, LogContentType(value).Valid(), LogContentType(value).String())
		}
		want = map[ResultStatus]string{ResultSucceeded: "succeeded", ResultFailed: "failed", ResultCanceled: "canceled", ResultRejected: "rejected", ResultSkipped: "skipped"}[ResultStatus(value)]
		if ResultStatus(value).Valid() != (value == 0 || want != "") || ResultStatus(value).String() != want {
			t.Fatalf("result %d: valid=%v string=%q", value, ResultStatus(value).Valid(), ResultStatus(value).String())
		}
	}
	if LogRuntime != 1 || LogAudit != 2 || LogElnis != 3 || ContentText != 0 || ContentJSON != 1 || ResultUnspecified != 0 || ResultSucceeded != 1 || ResultSkipped != 5 {
		t.Fatal("enum values changed")
	}
}

func TestEmitLogCopiesSourceIdentity(t *testing.T) {
	var received []LogRecord
	connection, err := LogSubmitted.Observe(func(_ context.Context, record LogRecord) error {
		received = append(received, record)
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	values := []string{"source-session", "", "source-attempt", "source-request", "source-root"}
	ctx := contextinfo.WithExecution(context.Background(), contextinfo.Execution{SessionID: "context-session", RunID: "context-run", Attempt: "2", RequestID: "req", RootRequestID: "root"})
	record := LogRecord{Category: LogAudit, Level: slog.LevelWarn, ResultStatus: ResultFailed, Type: ContentJSON, Content: `{"reason":"call failed"}`,
		Identity: LogIdentity{SessionID: &values[0], RunID: &values[1], Attempt: &values[2], RequestID: &values[3], RootRequestID: &values[4]}}
	if err := EmitLog(ctx, record); err != nil {
		t.Fatal(err)
	}
	for i := range values {
		values[i] = "changed"
	}
	record.Content = "changed"
	if len(received) != 1 {
		t.Fatal(received)
	}
	got := received[0]
	if got.Level != slog.LevelWarn || got.ResultStatus != ResultFailed || got.Content != `{"reason":"call failed"}` {
		t.Fatal(got)
	}
	for i, value := range []*string{got.Identity.SessionID, got.Identity.RunID, got.Identity.Attempt, got.Identity.RequestID, got.Identity.RootRequestID} {
		want := []string{"source-session", "", "source-attempt", "source-request", "source-root"}[i]
		if value == nil || *value != want || value == &values[i] {
			t.Fatalf("identity %d: %v", i, value)
		}
	}
	if err := EmitLog(ctx, LogRecord{Category: LogRuntime}); err != nil {
		t.Fatal(err)
	}
	got = received[1]
	if got.Identity != (LogIdentity{}) {
		t.Fatalf("identity inferred from context: %+v", got.Identity)
	}
	values = []string{"partial-session", ""}
	if err := EmitLog(ctx, LogRecord{Category: LogRuntime, Identity: LogIdentity{SessionID: &values[0], RequestID: &values[1]}}); err != nil {
		t.Fatal(err)
	}
	values[0], values[1] = "changed", "changed"
	got = received[len(received)-1]
	if got.Identity.SessionID == nil || *got.Identity.SessionID != "partial-session" || got.Identity.SessionID == &values[0] ||
		got.Identity.RequestID == nil || *got.Identity.RequestID != "" || got.Identity.RequestID == &values[1] {
		t.Fatalf("supplied identities were not copied: %+v", got.Identity)
	}
	if got.Identity.RunID != nil || got.Identity.Attempt != nil || got.Identity.RootRequestID != nil {
		t.Fatalf("missing identities inferred from context: %+v", got.Identity)
	}
	for _, ctx := range []context.Context{context.Background(), contextinfo.WithExecution(context.Background(), contextinfo.Execution{})} {
		if err := EmitLog(ctx, LogRecord{Category: LogRuntime}); err != nil {
			t.Fatal(err)
		}
		got = received[len(received)-1]
		if got.Identity != (LogIdentity{}) || got.ResultStatus != ResultUnspecified {
			t.Fatalf("manufactured absent facts: %+v", got)
		}
	}
}

func TestEmitLogValidatesContract(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = file
	t.Cleanup(func() { os.Stderr = old; _ = file.Close() })
	var received []LogRecord
	connection, err := LogSubmitted.Observe(func(_ context.Context, record LogRecord) error { received = append(received, record); return nil }, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	for name, record := range map[string]LogRecord{
		"missing category":    {},
		"invalid category":    {Category: LogCategory(255)},
		"invalid type":        {Category: LogRuntime, Type: LogContentType(255)},
		"invalid result":      {Category: LogRuntime, ResultStatus: ResultStatus(255)},
		"negative text limit": {Category: LogRuntime, ContentLimit: -1},
		"negative JSON limit": {Category: LogRuntime, Type: ContentJSON, Content: "null", ContentLimit: -1},
		"empty JSON":          {Category: LogRuntime, Type: ContentJSON},
		"malformed JSON":      {Category: LogRuntime, Type: ContentJSON, Content: "{sensitive-invalid-body"},
		"trailing JSON":       {Category: LogRuntime, Type: ContentJSON, Content: "{} {}"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := EmitLog(context.Background(), record); err == nil {
				t.Fatal("invalid record accepted")
			}
		})
	}
	if len(received) != 0 {
		t.Fatalf("invalid records dispatched: %v", received)
	}
	data, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "invalid log record") != 9 || strings.Contains(string(data), "sensitive-invalid-body") {
		t.Fatalf("invalid diagnostics: %s", data)
	}
	for _, record := range []LogRecord{
		{Category: LogRuntime},
		{Category: LogAudit, Level: slog.LevelWarn, ResultStatus: ResultFailed, Content: "reason"},
		{Category: LogElnis, Level: slog.LevelError, ResultStatus: ResultCanceled},
		{Category: LogAudit, Type: ContentJSON, Content: "null"},
		{Category: LogAudit, Type: ContentJSON, Content: "9007199254740993", ContentLimit: 1},
	} {
		if err := EmitLog(context.Background(), record); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(received[len(received)-1], record) {
			t.Fatalf("business facts changed: %+v", received)
		}
	}
	for _, record := range received {
		if record.Type == ContentJSON && !json.Valid([]byte(record.Content)) {
			t.Fatal(record)
		}
	}
	connection.Disconnect()
	if err := EmitLog(context.Background(), LogRecord{Category: LogRuntime}); err != nil {
		t.Fatalf("no subscribers: %v", err)
	}
}
