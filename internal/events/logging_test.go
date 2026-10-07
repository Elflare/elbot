package events

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/signal"
)

type mutableError struct{ text string }

func (e *mutableError) Error() string { return e.text }
func (e *mutableError) LogDiagnostic() LogDiagnostic {
	return LogDiagnostic{Kind: "upstream", Detail: e.text}
}

type lazyLog struct{ text *string }

func (v lazyLog) LogValue() slog.Value { return slog.StringValue(*v.text) }

func TestEmitLogFreezesPayloadAndExecution(t *testing.T) {
	var received []LogRecord
	connection, err := LogSubmitted.Connect(func(_ context.Context, record LogRecord) error { received = append(received, record); return nil }, signal.ConnectOptions{Shutdown: signal.CancelPending})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	values := []string{"original"}
	nested := map[string]any{"values": values}
	failure := &mutableError{text: "original error"}
	lazy := "original lazy"
	at := time.Now().Add(-time.Hour)
	fields := []slog.Attr{
		slog.String("session_id", "source-session"), slog.String("run_id", ""),
		slog.Any("nested", nested), slog.Any("error", fmt.Errorf("wrapped: %w", failure)),
		slog.Group("group", slog.Any("lazy", lazyLog{&lazy})),
	}
	ctx := contextinfo.WithExecution(context.Background(), contextinfo.Execution{SessionID: "context-session", RunID: "context-run", Attempt: "2", RequestID: "req", RootRequestID: "root"})
	if err := EmitLog(ctx, LogRecord{At: at, Category: LogAudit, Fields: fields}); err != nil {
		t.Fatal(err)
	}
	values[0] = "changed"
	nested["new"] = "changed"
	failure.text = "changed"
	lazy = "changed"
	fields[0] = slog.String("session_id", "changed")
	if len(received) != 1 || received[0].At != at {
		t.Fatal(received)
	}
	got := make(map[string]slog.Value)
	for _, attr := range received[0].Fields {
		got[attr.Key] = attr.Value
	}
	for key, want := range map[string]string{"session_id": "source-session", "run_id": "", "attempt": "2", "request_id": "req", "root_request_id": "root"} {
		if got[key].String() != want {
			t.Fatalf("%s=%s want %s", key, got[key], want)
		}
	}
	if !reflect.DeepEqual(got["nested"].Any(), map[string]any{"values": []any{"original"}}) {
		t.Fatal(got["nested"])
	}
	diagnostic := got["error"].Any().(map[string]any)
	if diagnostic["message"] != "wrapped: original error" || diagnostic["detail"] != "original error" || diagnostic["kind"] != "upstream" {
		t.Fatal(diagnostic)
	}
	if got["group"].Group()[0].Value.String() != "original lazy" {
		t.Fatal(got["group"])
	}
	before := time.Now()
	if err := EmitLog(context.Background(), LogRecord{}); err != nil {
		t.Fatal(err)
	}
	if received[1].At.Before(before) || len(received[1].Fields) != 0 {
		t.Fatal(received[1])
	}
}

func TestEmitLogBoundsCyclesAndUnsupportedObjects(t *testing.T) {
	cycle := map[string]any{}
	cycle["self"] = cycle
	connection, err := LogSubmitted.Connect(func(_ context.Context, record LogRecord) error {
		if record.Fields[1].Value.String() != "[unsupported log value]" {
			t.Fatal(record.Fields[1])
		}
		return nil
	}, signal.ConnectOptions{Shutdown: signal.CancelPending})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	if err := EmitLog(context.Background(), LogRecord{Fields: []slog.Attr{slog.Any("cycle", cycle), slog.Any("function", func() {})}}); err != nil {
		t.Fatal(err)
	}
}

func TestEmitLogKeepsSourceIdentityWhenPayloadExhaustsSnapshotBudget(t *testing.T) {
	connection, err := LogSubmitted.Connect(func(_ context.Context, record LogRecord) error {
		for _, attr := range record.Fields {
			if attr.Key == "session_id" {
				if attr.Value.String() != "source" {
					t.Fatalf("source identity replaced: %v", attr)
				}
				return nil
			}
		}
		t.Fatal("source identity lost")
		return nil
	}, signal.ConnectOptions{Shutdown: signal.CancelPending})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	ctx := contextinfo.WithExecution(context.Background(), contextinfo.Execution{SessionID: "context"})
	if err := EmitLog(ctx, LogRecord{Fields: []slog.Attr{slog.Any("large", make([]int, 20000)), slog.String("session_id", "source")}}); err != nil {
		t.Fatal(err)
	}
}

func TestEmitLogSnapshotsRawJSONAsStructuredValues(t *testing.T) {
	raw := json.RawMessage(`{"api_key":"secret","count":123}`)
	var got any
	connection, err := LogSubmitted.Connect(func(_ context.Context, record LogRecord) error { got = record.Fields[0].Value.Any(); return nil }, signal.ConnectOptions{Shutdown: signal.CancelPending})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	if err := EmitLog(context.Background(), LogRecord{Fields: []slog.Attr{slog.Any("raw", raw)}}); err != nil {
		t.Fatal(err)
	}
	clear(raw)
	want := map[string]any{"api_key": "secret", "count": json.Number("123")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("raw JSON must remain inspectable for redaction: %#v", got)
	}
}
