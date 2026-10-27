package cli

import (
	"context"
	"errors"
	"testing"
	"time"

	"elbot/internal/platform/share"
	"elbot/internal/signal"
)

func TestStartRacesBeginClose(t *testing.T) {
	for _, kind := range []string{"local", "remote"} {
		t.Run(kind, func(t *testing.T) {
			for range 12 {
				var adapter share.Runtime
				if kind == "local" {
					adapter, _ = testLocalAdapter(t)
				} else {
					adapter = testRemoteServer(t)
				}
				gate := make(chan struct{})
				result := make(chan error, 1)
				stopped := make(chan struct{})
				go func() { <-gate; result <- adapter.Start(t.Context(), nil) }()
				go func() { <-gate; adapter.BeginClose(); close(stopped) }()
				close(gate)
				err := <-result
				if err != nil && !errors.Is(err, signal.ErrClosed) && !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				<-stopped
				deadline, cancel := context.WithTimeout(t.Context(), time.Second)
				if err := adapter.Wait(deadline); err != nil {
					cancel()
					t.Fatal("stop became runtime failure", err)
				}
				if err := adapter.Close(deadline); err != nil {
					cancel()
					t.Fatal(err)
				}
				cancel()
			}
		})
	}
}
