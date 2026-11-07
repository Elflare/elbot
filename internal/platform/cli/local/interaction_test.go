package local

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"elbot/internal/platform/share"
	"elbot/internal/signal"
)

func testLocalAdapter(t *testing.T) (*Runtime, *os.File) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	a := New("cli", "user")
	a.input, a.interactive = reader, false
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := a.Close(ctx); err != nil {
			t.Error(err)
		}
		reader.Close()
		writer.Close()
	})
	return a, writer
}

func TestLocalStopUnblocksPipeAndKeepsInputOwnedByCaller(t *testing.T) {
	a, writer := testLocalAdapter(t)
	if err := a.Start(t.Context(), func(context.Context, string, share.MessageContext) error { t.Error("unexpected input"); return nil }); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := a.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	a.BeginClose()
	deadline, stop := context.WithTimeout(t.Context(), time.Second)
	defer stop()
	if err := a.Wait(deadline); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(deadline); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.WriteString("still owned\n"); err != nil {
		t.Fatal("stdin closed", err)
	}
	if err := a.Start(t.Context(), nil); !errors.Is(err, signal.ErrClosed) {
		t.Fatal(err)
	}
}

func TestLocalCloseTimeoutKeepsWorkAndCanRetry(t *testing.T) {
	a, writer := testLocalAdapter(t)
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	if err := a.Start(t.Context(), func(ctx context.Context, text string, msg share.MessageContext) error {
		if text != "hello" || msg.RawText != text || msg.Sender != a || msg.Conversation.Identity.ID != "local" {
			t.Error(text, msg)
		}
		close(entered)
		<-release
		return ctx.Err()
	}); err != nil {
		t.Fatal(err)
	}
	writer.WriteString("hello\n")
	<-entered
	a.BeginClose()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if err := a.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestLocalConcurrentStartStopAndClose(t *testing.T) {
	a, _ := testLocalAdapter(t)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := a.Start(t.Context(), nil); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	for range 8 {
		wg.Go(func() {
			a.BeginClose()
			if err := a.Close(t.Context()); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
}

func TestLocalRegularFileAndStartFailure(t *testing.T) {
	t.Run("regular file", func(t *testing.T) {
		file, err := os.CreateTemp(t.TempDir(), "input")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		file.WriteString("/exit\n")
		file.Seek(0, 0)
		a := New("cli", "user")
		a.input, a.interactive = file, false
		defer a.Close(t.Context())
		if err := a.Start(t.Context(), nil); err != nil {
			t.Fatal(err)
		}
		if err := a.Wait(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("closed input", func(t *testing.T) {
		a, _ := testLocalAdapter(t)
		a.input.Close()
		if err := a.Start(t.Context(), nil); !errors.Is(err, os.ErrClosed) {
			t.Fatal(err)
		}
		if err := a.Wait(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("never started", func(t *testing.T) {
		a, _ := testLocalAdapter(t)
		if err := a.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
}
