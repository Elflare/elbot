package signal

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestBackpressureFIFO(t *testing.T) {
	q, err := NewQueue(QueueOptions{Capacity: 1, WaitForCapacity: true, Logger: testLogger()})
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	var got []string
	submit(t, q, context.Background(), func(context.Context) error {
		close(started)
		<-release
		got = append(got, "A")
		return nil
	})
	<-started
	submit(t, q, context.Background(), func(context.Context) error { got = append(got, "B"); return nil })
	result := make(chan error, 1)
	go func() {
		result <- q.Submit(context.Background(), Task{Shutdown: Drain, Run: func(context.Context) error { got = append(got, "C"); return nil }})
	}()
	select {
	case err := <-result:
		t.Fatalf("full queue did not wait: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if err := q.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"A", "B", "C"}) {
		t.Fatal(got)
	}
}

func TestBackpressureWakeOnCancelAndClose(t *testing.T) {
	for _, closing := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "close"}[closing], func(t *testing.T) {
			q, err := NewQueue(QueueOptions{Capacity: 1, WaitForCapacity: true, Logger: testLogger()})
			if err != nil {
				t.Fatal(err)
			}
			started, release := make(chan struct{}), make(chan struct{})
			submit(t, q, context.Background(), func(context.Context) error { close(started); <-release; return nil })
			<-started
			submit(t, q, context.Background(), func(context.Context) error { return nil })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() {
				result <- q.Submit(ctx, Task{Shutdown: Drain, Run: func(context.Context) error { t.Error("unaccepted task ran"); return nil }})
			}()
			select {
			case err := <-result:
				t.Fatalf("full queue did not wait: %v", err)
			case <-time.After(20 * time.Millisecond):
			}
			want := context.Canceled
			if closing {
				q.BeginClose()
				want = ErrClosed
			} else {
				cancel()
			}
			select {
			case err := <-result:
				if !errors.Is(err, want) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("producer stuck")
			}
			select {
			case <-q.Done():
				t.Fatal("running callback declared stopped")
			default:
			}
			close(release)
			if err := q.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
