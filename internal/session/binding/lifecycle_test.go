package binding

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"elbot/internal/session/share"
	"elbot/internal/signal"
)

func TestCloseWaitsForLeaseAndPreservesCommittedEvent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := newTestManager(t)
		scope := share.Scope{ActorID: "u"}
		var event BindingChangedEvent
		_, err := m.ObserveBindingChanged(func(_ context.Context, e BindingChangedEvent) error {
			event = e
			return nil
		}, signal.ConnectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		ctx, release, err := m.EnterScope(context.Background(), scope)
		if err != nil {
			t.Fatal(err)
		}
		m.SetCurrent(ctx, scope, "one")
		binding := m.CurrentBinding(scope)
		waiting := make(chan error, 1)
		go func() {
			_, release, err := m.EnterScope(context.Background(), scope)
			if err == nil {
				release()
			}
			waiting <- err
		}()
		synctest.Wait()
		m.BeginClose()
		if err := <-waiting; !errors.Is(err, signal.ErrClosed) {
			t.Fatalf("waiting admission: %v", err)
		}
		if _, _, err := m.EnterSessions(context.Background(), "one"); !errors.Is(err, signal.ErrClosed) {
			t.Fatalf("new admission: %v", err)
		}
		if _, err := m.ObserveBindingChanged(func(context.Context, BindingChangedEvent) error { return nil }, signal.ConnectOptions{}); !errors.Is(err, signal.ErrClosed) {
			t.Fatalf("new subscription: %v", err)
		}
		deadline, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := m.Close(deadline); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("close before release: %v", err)
		}
		select {
		case <-m.Done():
			t.Fatal("Done before lease release")
		default:
		}
		release()
		if err := m.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		if event.New != binding || binding.Valid() || m.CurrentBinding(scope) != nil {
			t.Fatal("committed event lost or binding retained after close")
		}
		assertEmptyAdmissions(t, m)
	})
}

func TestCloseWaitsForActualObserverExit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := newTestManager(t)
		started, finish := make(chan struct{}), make(chan struct{})
		_, err := m.ObserveBindingChanged(func(context.Context, BindingChangedEvent) error {
			close(started)
			<-finish // 故意忽略信号执行期限，验证实际退出语义。
			return nil
		}, signal.ConnectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		published := make(chan struct{})
		go func() {
			setCurrent(t, m, share.Scope{ActorID: "u"}, "one")
			close(published)
		}()
		<-started
		// Emit 超时返回后，观察回调仍未退出。
		time.Sleep(31 * time.Second)
		<-published
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := m.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("close while observer is running: %v", err)
		}
		select {
		case <-m.Done():
			t.Fatal("Done before actual observer exit")
		default:
		}
		close(finish)
		if err := m.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
}

func TestUnusedManagerConcurrentClose(t *testing.T) {
	m := newTestManager(t)
	var workers sync.WaitGroup
	for range 12 {
		workers.Go(func() {
			m.BeginClose()
			if err := m.Close(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}
	workers.Wait()
	select {
	case <-m.Done():
	default:
		t.Fatal("unused manager did not finish closing")
	}
}
