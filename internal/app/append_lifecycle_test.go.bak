package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"elbot/internal/llm/chatcompletions"
)

type appendShutdownModel struct {
	assemblyModel
	started chan struct{}
}

func (m *appendShutdownModel) Stream(ctx context.Context, _ chatcompletions.Request) (<-chan chatcompletions.Chunk, error) {
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
	runtime, err := (defaultWiringFactory{}).Build(parent, req)
	t.Cleanup(func() { closeAssembledRuntime(t, runtime) })
	if err != nil {
		t.Fatal(err)
	}
	first := make(chan error, 1)
	go func() { first <- runtime.Dialogue.Handler().HandleMessage(parent, "first") }()
	select {
	case <-model.started:
	case <-time.After(time.Second):
		t.Fatal("model did not start")
	}
	if err := runtime.Dialogue.Handler().HandleMessage(parent, "append"); err != nil {
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
	case <-runtime.Dialogue.Execution().Done():
	case <-time.After(time.Second):
		t.Fatal("production app context did not stop append wait")
	}
	closeAssembledRuntime(t, runtime)
	if strings.Contains(platform.text(), "追加确认已过期") {
		t.Fatal("shutdown emitted expiry notification")
	}
	select {
	case <-runtime.Lifecycle.Done():
	default:
		t.Fatal("runtime workers remain after close")
	}
}
