package app

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"elbot/internal/config"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/signal"
)

type blockedNamingClient struct {
	assemblyModel
	started, release chan struct{}
}

func (m *blockedNamingClient) GenerateText(context.Context, llm.TextRequest) (llm.TextResult, error) {
	close(m.started)
	<-m.release
	return llm.TextResult{Text: "late title"}, nil
}
func TestWiringRetainsHookAndToolUntilDialogueNamingActuallyExits(t *testing.T) {
	req, _, _ := runtimeAssemblyFixture(t)
	req.Foundation.Config.Session.Naming.TriggerStep = 1
	model := &blockedNamingClient{started: make(chan struct{}), release: make(chan struct{})}
	req.Models.ByProvider["test"] = model
	c, err := (defaultWiringFactory{}).Build(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeAssembledRuntime(t, c) })
	release := sync.OnceFunc(func() { close(model.release) })
	t.Cleanup(release)
	if err := c.Dialogue.Start(); err != nil {
		t.Fatal(err)
	}
	if err := c.Dialogue.Handler().HandleMessage(t.Context(), "start naming"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-model.started:
	case <-time.After(time.Second):
		t.Fatal("naming not started")
	}
	lc := c.Lifecycle.(*wiringLifecycle)
	var order []string
	for _, g := range lc.groups() {
		order = append(order, g.name)
	}
	if !reflect.DeepEqual(order, []string{"platform", "background", "dialogue", "hook", "tool"}) {
		t.Fatal(order)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	if err := lc.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-c.Hook.Done():
		t.Fatal("Hook closed while Dialogue still holds it")
	default:
	}
	select {
	case <-lc.Done():
		t.Fatal("wiring released its dependencies before Dialogue exited")
	default:
	}
	release()
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := lc.Close(ctx); err != nil {
		t.Fatal(err)
	}
	for _, g := range lc.groups() {
		select {
		case <-g.owner.Done():
		default:
			t.Fatal("module remains live", g.name)
		}
	}
	if err := lc.Close(ctx); err != nil {
		t.Fatal(err)
	}
}
func TestServiceWithoutForegroundPlatformsWaitsForElnis(t *testing.T) {
	req, _, _ := runtimeAssemblyFixture(t)
	cfg := req.Foundation.Config
	cfg.Platform = config.PlatformConfig{}
	cfg.Elnis.Enabled = true
	cfg.Elnis.HTTP.Addr = "127.0.0.1:0"
	cfg.Elnis.Tokens = map[string]config.ElnisTokenConfig{"test": {TokenEnv: []string{"ELBOT_MODULE_TEST_TOKEN"}}}
	t.Setenv("ELBOT_MODULE_TEST_TOKEN", "test-token")
	var stages []string
	d := DefaultDependencies()
	d.Environment = environmentStub{events: &stages, mode: RunModeService, profiler: profilerStub{}}
	req.Foundation.Lifecycle = lifecycleFunc(func(context.Context) error { return nil })
	d.Foundation = foundationFactoryFunc(func(context.Context, FoundationRequest) (*FoundationComponents, error) { return req.Foundation, nil })
	d.Models = modelFactoryFunc(func(ModelRequest) (ModelClients, error) { return req.Models, nil })
	ready := make(chan struct{})
	conn, err := events.LogSubmitted.Connect(func(_ context.Context, r events.LogRecord) error {
		if r.EventName == "elbot_startup_completed" {
			close(ready)
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Disconnect()
	r, err := NewRunner(d)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- r.Run(ctx, Options{}) }()
	select {
	case <-ready:
	case err := <-result:
		t.Fatal("service exited during startup", err)
	case <-time.After(time.Second):
		t.Fatal("service did not start")
	}
	select {
	case err := <-result:
		t.Fatal("service ignored Elnis runtime", err)
	default:
	}
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("service did not join shutdown")
	}
}
