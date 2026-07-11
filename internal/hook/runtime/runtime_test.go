package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

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
	reader := bufio.NewReader(os.Stdin)
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
			writeHelperResponse(id, map[string]any{})
		case "event.handle":
			eventCount++
			if eventCount == 1 {
				writeHelperResponse(id, map[string]any{"status": "waiting", "conversation_id": "demo", "expires_at": time.Now().Add(10 * time.Second).UTC().Format(time.RFC3339Nano)})
			} else {
				writeHelperResponse(id, map[string]any{"status": "completed"})
			}
		case "system.shutdown":
			writeHelperResponse(id, map[string]any{})
			os.Exit(0)
		default:
			writeHelperError(id, "unknown method")
		}
	}
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
		Stateful:               true,
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
	if _, err := manager.Handle(context.Background(), "demo", event); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	handled, outputs, err := manager.Route(context.Background(), event)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if !handled || len(outputs) != 0 {
		t.Fatalf("Route = handled=%v outputs=%#v", handled, outputs)
	}
	handled, outputs, err = manager.Route(context.Background(), event)
	if err != nil {
		t.Fatalf("Route after completion: %v", err)
	}
	if handled || len(outputs) != 0 {
		t.Fatalf("Route after completion = handled=%v outputs=%#v", handled, outputs)
	}
	if err := manager.Stop(context.Background(), "demo"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func TestManagerBlockedWaitingConversationFallsThrough(t *testing.T) {
	manager := NewManager(Options{SharedDir: t.TempDir()})
	config := Config{
		Stateful:               true,
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
	if _, err := manager.Handle(context.Background(), "demo", event); err != nil {
		t.Fatalf("blocked Handle: %v", err)
	}
	if manager.hasLease(event) {
		t.Fatal("blocked Handle created a waiting lease")
	}
	manager.worker("demo").config.Block = hook.BlockPolicy{}
	if _, err := manager.Handle(context.Background(), "demo", event); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	manager.worker("demo").config.Block = policy
	handled, outputs, err := manager.Route(context.Background(), event)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if handled || len(outputs) != 0 || manager.hasLease(event) {
		t.Fatalf("blocked route = handled=%v outputs=%#v leased=%v", handled, outputs, manager.hasLease(event))
	}
}

func runtimeHelperCommand() string {
	return strings.Join([]string{os.Args[0], "-test.run=TestHookRuntimeHelperProcess", "--", "hook-runtime-helper"}, " ")
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
			Stateful:               true,
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
		Stateful:               true,
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
	invalid.Command = ""
	if err := manager.Apply([]Config{invalid}); err == nil {
		t.Fatal("expected invalid config error")
	}
	infos := manager.List()
	if len(infos) != 1 || infos[0].ID != "current" || infos[0].Status != StatusReady {
		t.Fatalf("workers after invalid Apply = %#v", infos)
	}
}
