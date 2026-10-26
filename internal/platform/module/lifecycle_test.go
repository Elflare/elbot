// lifecycle_test.go 验证旧平台装配或运行行为。
// TODO：对应行为迁入新边界的测试后删除本文件。

package module

import (
	"context"
	"errors"
	"testing"
	"time"

	"elbot/internal/platform"
	platformbuiltin "elbot/internal/platform/builtin"
)

type runtimeWithShutdownGate struct {
	fakeRuntimeWithCaller
	started, release chan struct{}
	stop             bool
}

func (r *runtimeWithShutdownGate) Run(ctx context.Context, _ platform.PlatformHandler) error {
	close(r.started)
	<-ctx.Done()
	if r.release != nil {
		<-r.release
	}
	return ctx.Err()
}
func (r *runtimeWithShutdownGate) StopAppOnExit() bool { return r.stop }
func TestPlatformDeadlineReportsLiveWorkersAndRepeatedCloseJoinsSameDone(t *testing.T) {
	m, err := NewModule(t.Context(), ModuleDeps{Mode: platformbuiltin.ModeService})
	if err != nil {
		t.Fatal(err)
	}
	adapter := &runtimeWithShutdownGate{fakeRuntimeWithCaller: fakeRuntimeWithCaller{name: "gate"}, started: make(chan struct{}), release: make(chan struct{})}
	m.runtimes = []platform.Runtime{adapter}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	defer m.Close(ctx)
	if err := m.Start(nil, nil); err != nil {
		t.Fatal(err)
	}
	<-adapter.started
	budget, end := context.WithTimeout(context.Background(), 10*time.Millisecond)
	if err := m.Close(budget); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	end()
	done := m.Done()
	select {
	case <-done:
		t.Fatal("Done published while runtime still holds dependencies")
	default:
	}
	close(adapter.release)
	for range 2 {
		if err := m.Close(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if m.Done() != done {
		t.Fatal("completion channel replaced")
	}
	if err := m.Start(nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatal("closed module restarted", err)
	}
}
func TestStopAppOnExitCancelsPeersAndNotifiesUpperOwner(t *testing.T) {
	m, err := NewModule(t.Context(), ModuleDeps{Mode: platformbuiltin.ModeCLIOnly})
	if err != nil {
		t.Fatal(err)
	}
	peer := &runtimeWithShutdownGate{fakeRuntimeWithCaller: fakeRuntimeWithCaller{name: "peer"}, started: make(chan struct{})}
	m.runtimes = []platform.Runtime{peer, exitRuntime{fakeRuntimeWithCaller{name: "exit"}}}
	stopped := make(chan struct{})
	if err := m.Start(nil, func() { close(stopped) }); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	defer m.Close(ctx)
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("application stop not requested")
	}
	if err := m.Wait(ctx); err != nil {
		t.Fatal(err)
	}
}

type exitRuntime struct{ fakeRuntimeWithCaller }

func (exitRuntime) StopAppOnExit() bool { return true }
