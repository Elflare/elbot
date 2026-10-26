// 验证 Hook 根包的阶段关闭、实际退出和独立平台订阅生命周期。

package hook

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	agenthooks "elbot/internal/agent"
	"elbot/internal/events"
	"elbot/internal/hook/pipeline"
	"elbot/internal/hook/share"
)

type startTestControl struct {
	*Supervisor
	reload func() (share.ReloadReport, error)
}

func (c startTestControl) HookReload() (share.ReloadReport, error) { return c.reload() }

func TestSupervisorConcurrentStartWaitsForPlatformSubscription(t *testing.T) {

	m, _ := newBridgeTestSupervisor(t)
	loading, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	loads := 0
	m.control = startTestControl{reload: func() (share.ReloadReport, error) {
		loads++
		close(loading)
		<-release
		return share.ReloadReport{}, nil
	}}
	connected := make(chan struct{}, 1)
	m.manager = bridgeTestPipeline{run: func(_ context.Context, event share.Event, _ pipeline.CallOptions) (share.Event, events.LogResult, error) {
		connected <- struct{}{}
		return event, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
	}}
	first, second := make(chan error, 1), make(chan error, 1)
	go func() { first <- m.Start() }()
	<-loading
	go func() { second <- m.Start() }()
	select {
	case err := <-second:
		t.Fatalf("second Start returned before subscriptions were ready: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	if _, err := events.PlatformConnected.Emit(context.Background(), events.PlatformConnectedEvent{Platform: "test"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-connected:
	case <-time.After(time.Second):
		t.Fatal("connection emitted after Start returned was lost")
	}
	if err := <-first; err != nil || loads != 1 {
		t.Fatalf("first Start=%v reloads=%d", err, loads)
	}
}

func TestSupervisorBeginClosePreservesAgentAndCloseWaitsForActualExit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, runtime := newBridgeTestSupervisor(t)
		entered := make(chan context.Context, 1)
		release := make(chan struct{})
		defer func() {
			select {
			case <-release:
			default:
				close(release)
			}
		}()
		m.manager = bridgeTestPipeline{run: func(ctx context.Context, event share.Event, _ pipeline.CallOptions) (share.Event, events.LogResult, error) {
			entered <- ctx
			<-release // Deliberately retain work after cancellation to test actual exit.
			return event, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
		}}
		agent := &bridgeTestAgent{}
		if err := m.ConnectAgent(agent); err != nil {
			t.Fatal(err)
		}
		m.BeginClose()
		if agent.disconnected != 0 {
			t.Fatal("BeginClose detached callbacks needed by Agent's draining producers")
		}
		if err := m.ConnectAgent(&bridgeTestAgent{}); err == nil {
			t.Fatal("new connection accepted after BeginClose")
		}
		callbackDone := make(chan struct{})
		go func() {
			_, _, _ = agent.points.AgentInputPrepared(context.Background(), agenthooks.Facts{}, agenthooks.Message{})
			close(callbackDone)
		}()
		runCtx := <-entered
		if runCtx.Err() != nil {
			t.Fatal("BeginClose canceled Agent's final callback")
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := m.Close(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("Close error=%v", err)
		}
		synctest.Wait()
		if agent.disconnected != 1 || runCtx.Err() == nil {
			t.Fatal("Close did not detach and cancel the admitted call")
		}
		select {
		case <-m.Done():
			t.Fatal("Done closed while callback was still running")
		case <-runtime.Done():
			t.Fatal("runtime closed while callback could still use it")
		default:
		}
		close(release)
		<-callbackDone
		<-m.Done()
		if err := m.Close(context.Background()); err != nil || agent.disconnected != 1 {
			t.Fatalf("repeated Close: disconnects=%d err=%v", agent.disconnected, err)
		}
	})
}

func TestSupervisorPlatformQueuesAreIndependentAndHaveNoAgentObserver(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, _ := newBridgeTestSupervisor(t)
		entered := make(chan string, 3)
		m.manager = bridgeTestPipeline{run: func(ctx context.Context, event share.Event, options pipeline.CallOptions) (share.Event, events.LogResult, error) {
			if options.Observe != nil || !options.Woken {
				t.Error("global platform event acquired Agent request tracking")
			}
			entered <- event.Platform.Name
			<-ctx.Done()
			return event, events.LogResult{ResultStatus: events.ResultCanceled}, ctx.Err()
		}}
		if err := m.ConnectAgent(&bridgeTestAgent{}); err != nil {
			t.Fatal(err)
		}
		if err := m.startPlatformEvents(context.Background()); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"one", "one", "two"} {
			_, err := events.PlatformConnected.Emit(context.Background(), events.PlatformConnectedEvent{Platform: name})
			if err != nil {
				t.Fatal(err)
			}
		}
		synctest.Wait()
		if len(entered) != 2 {
			t.Fatalf("expected one active callback per platform, got %d", len(entered))
		}
		first, second := <-entered, <-entered
		if first == second {
			t.Fatalf("same-platform callbacks ran concurrently: %q", first)
		}
		m.BeginClose()
		<-m.platformEventsDone()
		if len(entered) != 0 {
			t.Fatal("pending same-platform callback ran during shutdown")
		}
		if err := m.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
}
