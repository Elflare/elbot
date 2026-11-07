package cli

import (
	"context"
	"errors"
	"testing"
	"time"

	"elbot/internal/platform/cli/local"
	"elbot/internal/signal"
)

func TestStartRacesBeginClose(t *testing.T) {
	for range 12 {
		runtime, cleanup := testLocalRuntime(t)
		t.Cleanup(cleanup)
		gate := make(chan struct{})
		result := make(chan error, 1)
		stopped := make(chan struct{})
		go func() { <-gate; result <- runtime.Start(t.Context(), nil) }()
		go func() { <-gate; runtime.BeginClose(); close(stopped) }()
		close(gate)
		err := <-result
		if err != nil && !errors.Is(err, signal.ErrClosed) && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		<-stopped
		deadline, cancel := context.WithTimeout(t.Context(), time.Second)
		if err := runtime.Wait(deadline); err != nil {
			cancel()
			t.Fatal("stop became runtime failure", err)
		}
		if err := runtime.Close(deadline); err != nil {
			cancel()
			t.Fatal(err)
		}
		cancel()
	}
}

func testLocalRuntime(t *testing.T) (*local.Runtime, func()) {
	t.Helper()
	runtime := local.New(testPlatformName, "user")
	return runtime, func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := runtime.Close(ctx); err != nil {
			t.Error(err)
		}
	}
}
