package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"elbot/internal/delivery"
	"elbot/internal/hook"
)

func TestHookRuntimeHelperProcess(t *testing.T) {
	marker := -1
	for i := 0; i+1 < len(os.Args); i++ {
		if os.Args[i] == "--" && os.Args[i+1] == "hook-runtime-helper" {
			marker = i + 2
			break
		}
	}
	if marker == -1 {
		return
	}
	if !reflect.DeepEqual(os.Args[marker:], runtimeHelperArgs) {
		os.Exit(3)
	}
	reader := bufio.NewReader(os.Stdin)
	hookID := ""
	for eventCount := 0; ; {
		line, err := reader.ReadString('\n')
		if err != nil {
			os.Exit(0)
		}
		var frame map[string]any
		if json.Unmarshal([]byte(line), &frame) != nil {
			os.Exit(2)
		}
		if frame["type"] == "event" {
			continue
		}
		id, _ := frame["id"].(string)
		method, _ := frame["method"].(string)
		switch method {
		case "system.init":
			hookID = helperHookID(frame)
			writeHelperResponse(id, map[string]any{})
		case "event.handle":
			eventCount++
			if helperActorID(frame) == "test:output" {
				writeHelperResponse(id, map[string]any{"status": "completed", "outputs": []map[string]any{{"kind": "image", "base64": "aGVsbG8=", "name": "hello.png"}}, "target": map[string]any{"platform": "qqonebot", "group_id": "42"}, "timing": "after_assistant"})
			} else if eventCount == 1 {
				writeHelperResponse(id, map[string]any{"status": "waiting", "conversation_id": "demo", "expires_at": time.Now().Add(10 * time.Second).UTC().Format(time.RFC3339Nano)})
			} else if helperActorID(frame) == "test:defaults" {
				writeHelperResponse(id, map[string]any{"status": "completed"})
			} else {
				writeHelperResponse(id, map[string]any{"status": "completed", "pass_through": true})
			}
		case "system.shutdown":
			if hookID == "slow-shutdown" {
				time.Sleep(200 * time.Millisecond)
			}
			writeHelperResponse(id, map[string]any{})
			os.Exit(0)
		default:
			writeHelperError(id, "unknown method")
		}
	}
}

func helperHookID(value map[string]any) string {
	params, _ := value["params"].(map[string]any)
	hookValue, _ := params["hook"].(map[string]any)
	id, _ := hookValue["id"].(string)
	return id
}

func TestManagerBuildsCanonicalRuntimeOutputs(t *testing.T) {
	manager := NewManager(Options{SharedDir: t.TempDir()})
	config := Config{Mode: ModePersistent, Command: runtimeHelperCommand(), Cwd: ".", StartupTimeoutSeconds: 2, ShutdownTimeoutSeconds: 2, EventTimeoutSeconds: 2, MaxWaitSeconds: 30, Restart: RestartConfig{Strategy: "never", InitialDelaySeconds: 1, MaxDelaySeconds: 1}, ID: "demo", Dir: t.TempDir()}
	if err := manager.Apply([]Config{config}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	t.Cleanup(func() { manager.Close(context.Background()) })
	waitForStatus(t, manager, "demo", StatusReady)
	event := hook.Event{Actor: hook.ActorContext{ID: "test:output"}}
	got, err := manager.Handle(context.Background(), "demo", event, hook.Control{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Outputs) != 1 || string(got.Outputs[0].Source.Data) != "hello" || got.Outputs[0].Target.GroupID != "42" || got.Outputs[0].Target.Platform != "qqonebot" || delivery.DeliveryTiming(got.Outputs[0]) != delivery.DeliveryAfterAssistant {
		t.Fatalf("outputs = %#v", got.Outputs)
	}
}

type discardWriteCloser struct{}

func (discardWriteCloser) Write(data []byte) (int, error) { return len(data), nil }
func (discardWriteCloser) Close() error                   { return nil }

func TestWorkerRejectsOversizedInputFrame(t *testing.T) {
	w := &worker{stdin: discardWriteCloser{}}
	err := w.write(frame{Type: "request", Error: strings.Repeat("x", hook.MaxProtocolFrameBytes)})
	if err == nil || !strings.Contains(err.Error(), "stdin frame exceeds 16 MiB limit") {
		t.Fatalf("err = %v", err)
	}
}

func helperActorID(value map[string]any) string {
	params, _ := value["params"].(map[string]any)
	event, _ := params["event"].(map[string]any)
	actor, _ := event["actor"].(map[string]any)
	id, _ := actor["id"].(string)
	return id
}

func writeHelperResponse(id string, result any) {
	data, _ := json.Marshal(map[string]any{"type": "response", "id": id, "ok": true, "result": result})
	fmt.Fprintln(os.Stdout, string(data))
}

func writeHelperError(id, message string) {
	data, _ := json.Marshal(map[string]any{"type": "response", "id": id, "ok": false, "error": message})
	fmt.Fprintln(os.Stdout, string(data))
}

func TestManagerRoutesWaitingConversation(t *testing.T) {
	manager := NewManager(Options{SharedDir: t.TempDir()})
	config := Config{
		Mode:                   ModePersistent,
		Command:                runtimeHelperCommand(),
		Cwd:                    ".",
		StartupTimeoutSeconds:  2,
		ShutdownTimeoutSeconds: 2,
		EventTimeoutSeconds:    2,
		MaxWaitSeconds:         30,
		Restart:                RestartConfig{Strategy: "never", InitialDelaySeconds: 1, MaxDelaySeconds: 1},
		ID:                     "demo",
		Dir:                    t.TempDir(),
	}
	if err := manager.Apply([]Config{config}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	t.Cleanup(func() { manager.Close(context.Background()) })
	waitForStatus(t, manager, "demo", StatusReady)
	event := hook.Event{Platform: hook.PlatformContext{Name: "test", ScopeID: "private:1"}, Actor: hook.ActorContext{ID: "test:1", UserID: "1"}}
	if _, err := manager.Handle(context.Background(), "demo", event, hook.Control{Consume: true, StopPropagation: true}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	updated, handled, err := manager.Route(context.Background(), event)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if !handled || len(updated.Outputs) != 0 || updated.Control.Consume || updated.Control.StopPropagation {
		t.Fatalf("Route = handled=%v event=%#v", handled, updated)
	}
	updated, handled, err = manager.Route(context.Background(), event)
	if err != nil {
		t.Fatalf("Route after completion: %v", err)
	}
	if handled || len(updated.Outputs) != 0 {
		t.Fatalf("Route after completion = handled=%v event=%#v", handled, updated)
	}
	if _, err := manager.Stop(context.Background(), "demo"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func TestManagerReclaimsTransientWorkerAfterPassThrough(t *testing.T) {
	manager := NewManager(Options{SharedDir: t.TempDir()})
	config := Config{
		Mode:                   ModeTransient,
		Command:                runtimeHelperCommand(),
		Cwd:                    ".",
		StartupTimeoutSeconds:  2,
		ShutdownTimeoutSeconds: 2,
		EventTimeoutSeconds:    2,
		MaxWaitSeconds:         30,
		Restart:                RestartConfig{Strategy: "never", InitialDelaySeconds: 1, MaxDelaySeconds: 1},
		ID:                     "demo",
		Dir:                    t.TempDir(),
	}
	if err := manager.Apply([]Config{config}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	t.Cleanup(func() { manager.Close(context.Background()) })
	event := hook.Event{Platform: hook.PlatformContext{Name: "test", ScopeID: "private:transient"}, Actor: hook.ActorContext{ID: "test:transient", UserID: "transient"}}
	if _, err := manager.Handle(context.Background(), "demo", event, hook.Control{Consume: true, StopPropagation: true}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	manager.mu.RLock()
	active := len(manager.transient)
	manager.mu.RUnlock()
	if active != 1 || !manager.hasLease(event) {
		t.Fatalf("transient worker = %d leased=%v, want one leased worker", active, manager.hasLease(event))
	}
	updated, routed, err := manager.Route(context.Background(), event)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if !routed || updated.Control.Consume || updated.Control.StopPropagation {
		t.Fatalf("Route = routed=%v control=%#v", routed, updated.Control)
	}
	manager.mu.RLock()
	active = len(manager.transient)
	manager.mu.RUnlock()
	if active != 0 || manager.hasLease(event) {
		t.Fatalf("transient worker = %d leased=%v after completion", active, manager.hasLease(event))
	}
}

func TestManagerStopReclaimsTransientWorkers(t *testing.T) {
	manager := NewManager(Options{SharedDir: t.TempDir()})
	config := Config{
		Mode:                   ModeTransient,
		Command:                runtimeHelperCommand(),
		Cwd:                    ".",
		StartupTimeoutSeconds:  2,
		ShutdownTimeoutSeconds: 2,
		EventTimeoutSeconds:    2,
		MaxWaitSeconds:         30,
		Restart:                RestartConfig{Strategy: "never", InitialDelaySeconds: 1, MaxDelaySeconds: 1},
		ID:                     "demo",
		Dir:                    t.TempDir(),
	}
	if err := manager.Apply([]Config{config}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	t.Cleanup(func() { manager.Close(context.Background()) })
	event := hook.Event{Platform: hook.PlatformContext{Name: "test", ScopeID: "private:stop"}, Actor: hook.ActorContext{ID: "test:stop", UserID: "stop"}}
	if _, err := manager.Handle(context.Background(), "demo", event, hook.Control{}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if _, err := manager.Stop(context.Background(), "demo"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	manager.mu.RLock()
	active := len(manager.transient)
	manager.mu.RUnlock()
	if active != 0 || manager.hasLease(event) {
		t.Fatalf("transient worker = %d leased=%v after stop", active, manager.hasLease(event))
	}
}

func TestManagerRouteUsesConfiguredDefaults(t *testing.T) {
	manager := NewManager(Options{SharedDir: t.TempDir()})
	config := Config{
		Mode:                   ModePersistent,
		Command:                runtimeHelperCommand(),
		Cwd:                    ".",
		StartupTimeoutSeconds:  2,
		ShutdownTimeoutSeconds: 2,
		EventTimeoutSeconds:    2,
		MaxWaitSeconds:         30,
		Restart:                RestartConfig{Strategy: "never", InitialDelaySeconds: 1, MaxDelaySeconds: 1},
		ID:                     "demo",
		Dir:                    t.TempDir(),
	}
	if err := manager.Apply([]Config{config}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	t.Cleanup(func() { manager.Close(context.Background()) })
	waitForStatus(t, manager, "demo", StatusReady)
	event := hook.Event{Platform: hook.PlatformContext{Name: "test", ScopeID: "private:defaults"}, Actor: hook.ActorContext{ID: "test:defaults", UserID: "defaults"}}
	defaults := hook.Control{Consume: true, StopPropagation: false}
	if _, err := manager.Handle(context.Background(), "demo", event, defaults); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	updated, routed, err := manager.Route(context.Background(), event)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if !routed || !updated.Control.Consume || updated.Control.StopPropagation {
		t.Fatalf("Route = routed=%v control=%#v", routed, updated.Control)
	}
}

func TestManagerHandleSkipsReleasedWaitingOwner(t *testing.T) {
	manager := NewManager(Options{})
	event := hook.Event{Metadata: map[string]any{skipHookIDMetadataKey: "demo"}}
	updated, err := manager.Handle(context.Background(), "demo", event, hook.Control{})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if updated.Metadata[skipHookIDMetadataKey] != "demo" {
		t.Fatalf("metadata = %#v", updated.Metadata)
	}
}

func TestManagerBlockedWaitingConversationFallsThrough(t *testing.T) {
	manager := NewManager(Options{SharedDir: t.TempDir()})
	config := Config{
		Mode:                   ModePersistent,
		Command:                runtimeHelperCommand(),
		Cwd:                    ".",
		StartupTimeoutSeconds:  2,
		ShutdownTimeoutSeconds: 2,
		EventTimeoutSeconds:    2,
		MaxWaitSeconds:         30,
		Restart:                RestartConfig{Strategy: "never", InitialDelaySeconds: 1, MaxDelaySeconds: 1},
		ID:                     "demo",
		Dir:                    t.TempDir(),
	}
	if err := manager.Apply([]Config{config}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	t.Cleanup(func() { manager.Close(context.Background()) })
	waitForStatus(t, manager, "demo", StatusReady)
	event := hook.Event{Platform: hook.PlatformContext{Name: "qqonebot", ScopeID: "group:123"}, Actor: hook.ActorContext{ID: "qqonebot:1", UserID: "1"}}
	policy, err := hook.NewBlockPolicy(nil, []string{"qqonebot:123"}, nil)
	if err != nil {
		t.Fatalf("NewBlockPolicy: %v", err)
	}
	manager.worker("demo").config.Block = policy
	if _, err := manager.Handle(context.Background(), "demo", event, hook.Control{}); err != nil {
		t.Fatalf("blocked Handle: %v", err)
	}
	if manager.hasLease(event) {
		t.Fatal("blocked Handle created a waiting lease")
	}
	manager.worker("demo").config.Block = hook.BlockPolicy{}
	if _, err := manager.Handle(context.Background(), "demo", event, hook.Control{}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	manager.worker("demo").config.Block = policy
	updated, handled, err := manager.Route(context.Background(), event)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if handled || len(updated.Outputs) != 0 || manager.hasLease(event) {
		t.Fatalf("blocked route = handled=%v event=%#v leased=%v", handled, updated, manager.hasLease(event))
	}
}

var runtimeHelperArgs = []string{"space arg", "", `C:\hook dir\`, `"quoted"`}

func runtimeHelperCommand() []string {
	return append([]string{os.Args[0], "-test.run=TestHookRuntimeHelperProcess", "--", "hook-runtime-helper"}, runtimeHelperArgs...)
}

func waitForStatus(t *testing.T, manager *Manager, id string, wanted Status) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, info := range manager.List() {
			if info.ID == id && info.Status == wanted {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("hook %q did not reach %s: %#v", id, wanted, manager.List())
}

func TestHookReloadRequestWaitsForActiveHandle(t *testing.T) {
	manager := NewManager(Options{SharedDir: t.TempDir()})
	committed := make(chan struct{})
	manager.SetPluginReloadPreparer(func(id string) (func() error, error) {
		if id != "demo" {
			t.Fatalf("reload id = %q, want demo", id)
		}
		return func() error {
			close(committed)
			return nil
		}, nil
	})
	worker := newWorker(manager, Config{ID: "demo"})
	worker.status = StatusRunning
	worker.active = 1
	result, err := worker.pluginRequest(frame{Method: "hooks.reload", Params: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("pluginRequest: %v", err)
	}
	if scheduled, _ := result.(map[string]any)["scheduled"].(bool); !scheduled {
		t.Fatalf("result = %#v", result)
	}
	worker.armReload()
	select {
	case <-committed:
		t.Fatal("reload committed before active handle finished")
	case <-time.After(20 * time.Millisecond):
	}
	worker.finishHandle()
	select {
	case <-committed:
	case <-time.After(time.Second):
		t.Fatal("reload was not committed after active handle finished")
	}
}

func TestManagerReplacePluginKeepsOtherWorker(t *testing.T) {
	manager := NewManager(Options{SharedDir: t.TempDir()})
	config := func(id string) Config {
		return Config{
			Mode:                   ModePersistent,
			Command:                runtimeHelperCommand(),
			Cwd:                    ".",
			StartupTimeoutSeconds:  2,
			ShutdownTimeoutSeconds: 2,
			EventTimeoutSeconds:    2,
			MaxWaitSeconds:         30,
			Restart:                RestartConfig{Strategy: "never", InitialDelaySeconds: 1, MaxDelaySeconds: 1},
			ID:                     id,
			Dir:                    t.TempDir(),
		}
	}
	demo := config("demo")
	other := config("other")
	if err := manager.Apply([]Config{demo, other}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	t.Cleanup(func() { manager.Close(context.Background()) })
	waitForStatus(t, manager, "demo", StatusReady)
	waitForStatus(t, manager, "other", StatusReady)
	oldDemo := manager.worker("demo")
	oldOther := manager.worker("other")
	moved := demo
	moved.Dir = t.TempDir()
	if err := manager.ReplacePlugin(moved); err == nil {
		t.Fatal("expected self reload to reject a plugin path change")
	}
	if err := manager.ReplacePlugin(demo); err != nil {
		t.Fatalf("ReplacePlugin: %v", err)
	}
	if manager.worker("demo") == oldDemo {
		t.Fatal("demo worker was not replaced")
	}
	if manager.worker("other") != oldOther {
		t.Fatal("other worker was replaced")
	}
}

func TestManagerApplyInvalidConfigKeepsCurrentWorkers(t *testing.T) {
	manager := NewManager(Options{SharedDir: t.TempDir()})
	config := Config{
		Mode:                   ModePersistent,
		Command:                runtimeHelperCommand(),
		Cwd:                    ".",
		StartupTimeoutSeconds:  2,
		ShutdownTimeoutSeconds: 2,
		EventTimeoutSeconds:    2,
		MaxWaitSeconds:         30,
		Restart:                RestartConfig{Strategy: "never", InitialDelaySeconds: 1, MaxDelaySeconds: 1},
		ID:                     "current",
		Dir:                    t.TempDir(),
	}
	if err := manager.Apply([]Config{config}); err != nil {
		t.Fatalf("Apply current: %v", err)
	}
	t.Cleanup(func() { manager.Close(context.Background()) })
	waitForStatus(t, manager, "current", StatusReady)

	invalid := config
	invalid.ID = "invalid"
	invalid.Command = nil
	if err := manager.Apply([]Config{invalid}); err == nil {
		t.Fatal("expected invalid config error")
	}
	infos := manager.List()
	if len(infos) != 1 || infos[0].ID != "current" || infos[0].Status != StatusReady {
		t.Fatalf("workers after invalid Apply = %#v", infos)
	}
}

func TestManagerCloseRejectsNewStartsAndIsIdempotent(t *testing.T) {
	manager := NewManager(Options{SharedDir: t.TempDir()})
	if err := manager.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := manager.Close(context.Background()); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if err := manager.Apply(nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("Apply after Close = %v, want ErrClosed", err)
	}
	if err := manager.Start("missing"); !errors.Is(err, ErrClosed) {
		t.Fatalf("Start after Close = %v, want ErrClosed", err)
	}
}

func TestManagerCloseContextOnlyBoundsWaiting(t *testing.T) {
	manager := NewManager(Options{SharedDir: t.TempDir()})
	config := Config{
		Mode:                   ModePersistent,
		Command:                runtimeHelperCommand(),
		Cwd:                    ".",
		StartupTimeoutSeconds:  2,
		ShutdownTimeoutSeconds: 2,
		EventTimeoutSeconds:    2,
		MaxWaitSeconds:         30,
		Restart:                RestartConfig{Strategy: "never", InitialDelaySeconds: 1, MaxDelaySeconds: 1},
		ID:                     "slow-shutdown",
		Dir:                    t.TempDir(),
	}
	if err := manager.Apply([]Config{config}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	waitForStatus(t, manager, config.ID, StatusReady)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := manager.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close = %v, want deadline exceeded", err)
	}
	if err := manager.Close(context.Background()); err != nil {
		t.Fatalf("eventual Close: %v", err)
	}
}
