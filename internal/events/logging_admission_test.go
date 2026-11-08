package events

import (
	"context"
	"elbot/internal/signal"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestLogSignalAdmissionDiagnosedWithoutDuplicatingConsumerFailure(t *testing.T) {
	if err := signal.FlushDiagnostics(t.Context()); err != nil {
		t.Fatal(err)
	}
	file, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	previous, stderr := LogSubmitted, os.Stderr
	source := signal.New[LogRecord, signal.Empty]("log.test", signal.Options[LogRecord]{Capacity: 1, Concurrency: 1})
	LogSubmitted, os.Stderr = source, file
	release, started := make(chan struct{}), make(chan struct{})
	defer func() {
		_ = source.Close(context.Background())
		_ = signal.FlushDiagnostics(context.Background())
		LogSubmitted, os.Stderr = previous, stderr
		_ = file.Close()
	}()
	failure := errors.New("consumer owns this failure")
	_, err = source.Observe(func(_ context.Context, record LogRecord) {
		if record.Content == "block" {
			close(started)
			<-release
		}

	}, signal.ConnectOptions{Shutdown: signal.Drain})
	if err != nil {
		t.Fatal(err)
	}
	Log(t.Context(), LogRecord{Category: LogCategoryLog})
	AsyncLog(t.Context(), LogRecord{Category: LogCategoryLog, Content: "block"})
	<-started
	AsyncLog(t.Context(), LogRecord{Category: LogCategoryLog})
	AsyncLog(t.Context(), LogRecord{Category: LogCategoryLog})
	close(release)
	if err := source.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := signal.FlushDiagnostics(t.Context()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "signal admission failed") != 1 || strings.Contains(string(data), failure.Error()) {
		t.Fatalf("diagnostics: %s", data)
	}
}
