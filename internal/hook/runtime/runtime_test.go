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
	if err := manager.Stop(context.Background(), "demo"); err != nil {
		t.Fatalf("Stop: %v", err)
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
