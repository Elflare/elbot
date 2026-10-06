package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"elbot/internal/llm"
)

type appendShutdownModel struct {
	assemblyModel
	started chan struct{}
}

func (m *appendShutdownModel) ChatStream(ctx context.Context, _ llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	close(m.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestProductionAssemblyClosesAppendWait(t *testing.T) {
	req, platform, _ := runtimeAssemblyFixture(t)
	model := &appendShutdownModel{started: make(chan struct{})}
	req.Models.ByProvider["test"] = model
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtime, err := (defaultRuntimeFactory{}).Build(parent, req)
	t.Cleanup(func() { closeAssembledRuntime(t, runtime) })
	if err != nil {
		t.Fatal(err)
	}
	first := make(chan error, 1)
	go func() { first <- runtime.Agent.HandleMessage(parent, "first") }()
	select {
	case <-model.started:
	case <-time.After(time.Second):
		t.Fatal("model did not start")
	}
	if err := runtime.Agent.HandleMessage(parent, "append"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-first:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("interrupted model did not exit")
	}
	if !strings.Contains(platform.text(), "是否追加") {
		t.Fatal("append confirmation was not offered")
	}
	cancel()
	select {
	case <-runtime.Agent.Done():
	case <-time.After(time.Second):
		t.Fatal("production app context did not stop append wait")
	}
	closeAssembledRuntime(t, runtime)
	if strings.Contains(platform.text(), "追加确认已过期") {
		t.Fatal("shutdown emitted expiry notification")
	}
	if !runtime.Lifecycle.(*runtimeLifecycle).stopped() {
		t.Fatal("runtime workers remain after close")
	}
}

type blockedAppendLifecycle struct{ done chan struct{} }

func (l blockedAppendLifecycle) Close(ctx context.Context) error {
	select {
	case <-l.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (l blockedAppendLifecycle) Done() <-chan struct{} { return l.done }

func TestRuntimeRetainsHookDependenciesUntilAppendOutputExits(t *testing.T) {
	req, _, _ := runtimeAssemblyFixture(t)
	runtime, err := (defaultRuntimeFactory{}).Build(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Agent.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	lifecycle := runtime.Lifecycle.(*runtimeLifecycle)
	blocked := blockedAppendLifecycle{done: make(chan struct{})}
	lifecycle.agent = blocked
	t.Cleanup(func() {
		close(blocked.done)
		closeAssembledRuntime(t, runtime)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := lifecycle.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close=%v", err)
	}
	if lifecycle.stopped() {
		t.Fatal("shutdown claimed completion with expiry output still running")
	}
	select {
	case <-lifecycle.hooks.Done():
		t.Fatal("released Hook runtime while expiry output was active")
	default:
	}
}
