// 验证插件运行时、工具审计和规范化输出行为。

package runtime

import toolshare "elbot/internal/tool/share"

import (
	"bufio"
	"context"
	"elbot/internal/processenv"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/hook/share"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/signal"
	tool "elbot/internal/tool/registry"
)

type auditTestTool struct{ err error }

func (auditTestTool) Name() string           { return "audit_test" }
func (auditTestTool) Info() toolshare.Info   { return toolshare.Info{Name: "audit_test"} }
func (auditTestTool) Schema() llm.ToolSchema { return llm.ToolSchema{Name: "audit_test"} }
func (a auditTestTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (*toolshare.PreparedCall, error) {
	return toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: a.Info().Risk}, "", func(ctx context.Context) (*toolshare.Result, string, error) {
		if a.err != nil {
			return nil, a.err.Error(), a.err
		}
		return nil, "tool completed", nil
	}), nil
}

func TestPluginToolAuditOutcome(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		level  slog.Level
		status string
	}{
		{"success", nil, slog.LevelInfo, "succeeded"}, {"failure", errors.New("failed"), slog.LevelError, "failed"},
		{"canceled", fmt.Errorf("wrapped: %w", context.Canceled), slog.LevelInfo, "canceled"}, {"timeout", context.DeadlineExceeded, slog.LevelError, "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var records []events.LogRecord
			connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
				if record.EventName == "hook_tool_call_completed" {
					records = append(records, record)
				}
				return nil
			}, signal.ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Disconnect()
			registry := tool.NewRegistry(security.DefaultPermission())
			if err := registry.Register(auditTestTool{err: tc.err}); err != nil {
				t.Fatal(err)
			}
			worker := &worker{manager: &Manager{rootCtx: context.Background(), opts: Options{Log: testLog, Notify: testNotify, Tools: testTools{registry}}}, config: share.RuntimeConfig{ID: "plugin", EventTimeoutSeconds: 10, Tools: share.RuntimeToolsConfig{BackgroundAllow: []string{"audit_test"}}}}
			_, err = worker.callTool(json.RawMessage(`{"name":"audit_test","arguments":{},"background":true,"target":{"platform":"cli"}}`))
			if !errors.Is(err, tc.err) {
				t.Fatalf("business error changed: %v", err)
			}
			if len(records) != 1 || records[0].EventName != "hook_tool_call_completed" || records[0].Category != events.LogAudit || records[0].Level != tc.level {
				t.Fatalf("records: %+v", records)
			}
			fields := map[string]any{}
			if err := json.Unmarshal([]byte(records[0].Content), &fields); err != nil {
				t.Fatal(err)
			}
			if fields["source"] != "plugin" || records[0].ResultStatus.String() != tc.status || (tc.err != nil && fields["result"] != tc.err.Error()) {
				t.Fatal(fields)
			}
		})
	}
}

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
			if frame["method"] == "event.cancel" && hookID == "cancel-aware" {
				continue
			}
			continue
		}
		id, _ := frame["id"].(string)
		method, _ := frame["method"].(string)
		switch method {
		case "system.init":
			hookID = helperHookID(frame)
			if hookID == "env" && os.Getenv("ELBOT_WORKER_HOOK_ENV_TEST") != "from-dotenv" {
				writeHelperError(id, "worker environment is missing")
				continue
			}
			writeHelperResponse(id, map[string]any{})
			if hookID == "blocked-log" {
				fmt.Fprintln(os.Stdout, `{"type":"event","method":"hook.log","params":{"message":"hold reader"}}`)
			}
		case "event.handle":
			if helperActorID(frame) == "test:cancel" {
				continue
			}
			eventCount++
			if helperActorID(frame) == "test:media" {
				fmt.Fprintln(os.Stdout, `{"type":"request","id":"plugin:media","method":"media.import","params":{"base64":"aGVsbG8=","name":"hello.png","mime_type":"image/png"}}`)
				line, err := reader.ReadString('\n')
				var response struct {
					OK     bool `json:"ok"`
					Result struct {
						MediaID string `json:"media"`
					} `json:"result"`
				}
				if err != nil || json.Unmarshal([]byte(line), &response) != nil || !response.OK {
					os.Exit(12)
				}
				writeHelperResponse(id, map[string]any{"status": "completed", "message": map[string]any{"segments": []map[string]any{{"type": "image", "base64": response.Result.MediaID}}}, "outputs": []map[string]any{{"kind": "image", "url": response.Result.MediaID}}})
				continue
			}
			if helperActorID(frame) == "test:output" {
				writeHelperResponse(id, map[string]any{"status": "completed", "outputs": []map[string]any{{"kind": "image", "base64": "aGVsbG8=", "name": "hello.png"}}, "target": map[string]any{"platform": "qqonebot", "group_id": "42"}, "timing": "after_assistant"})
			} else if helperActorID(frame) == "test:message" {
				writeHelperResponse(id, map[string]any{"status": "completed", "message": map[string]any{"segments": []map[string]any{
					{"type": "text", "text": "截图完成"},
					{"type": "image", "base64": "aGVsbG8=", "mime_type": "image/png"},
				}}})
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
	manager, err := NewManager(Options{Log: testLog, Notify: testNotify, SharedDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	config := share.RuntimeConfig{Mode: share.RuntimeModePersistent, Command: runtimeHelperCommand(), Cwd: ".", StartupTimeoutSeconds: 2, ShutdownTimeoutSeconds: 2, EventTimeoutSeconds: 2, MaxWaitSeconds: 30, Restart: share.RuntimeRestartConfig{Strategy: "never", InitialDelaySeconds: 1, MaxDelaySeconds: 1}, ID: "demo", Dir: t.TempDir()}
	if err := manager.Apply([]share.RuntimeConfig{config}, func(string) {}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	t.Cleanup(func() { manager.Close(context.Background()) })
	waitForStatus(t, manager, "demo", StatusReady)
	event := share.Event{Actor: share.ActorContext{ID: "test:output"}}
	got, _, err := manager.Handle(context.Background(), "demo", event, share.Control{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Outputs) != 1 || string(got.Outputs[0].Source.Data) != "hello" || got.Outputs[0].Target.GroupID != "42" || got.Outputs[0].Target.Platform != "qqonebot" || delivery.DeliveryTiming(got.Outputs[0]) != delivery.DeliveryAfterAssistant {
		t.Fatalf("outputs = %#v", got.Outputs)
	}
}

func TestManagerBuildsCanonicalRuntimeMessageSegments(t *testing.T) {
	manager, err := NewManager(Options{Log: testLog, Notify: testNotify, SharedDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	config := share.RuntimeConfig{Mode: share.RuntimeModePersistent, Command: runtimeHelperCommand(), Cwd: ".", StartupTimeoutSeconds: 2, ShutdownTimeoutSeconds: 2, EventTimeoutSeconds: 2, MaxWaitSeconds: 30, Restart: share.RuntimeRestartConfig{Strategy: "never", InitialDelaySeconds: 1, MaxDelaySeconds: 1}, ID: "demo", Dir: t.TempDir()}
	if err := manager.Apply([]share.RuntimeConfig{config}, func(string) {}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	t.Cleanup(func() { manager.Close(context.Background()) })
	waitForStatus(t, manager, "demo", StatusReady)
	event := share.Event{
		Point:   share.PointToolCallCompleted,
		Actor:   share.ActorContext{ID: "test:message"},
		Message: share.MessagePayload{Role: "tool"},
		Tool:    share.ToolPayload{Name: "screenshot"},
	}
	got, _, err := manager.Handle(context.Background(), "demo", event, share.Control{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Message.Segments) != 2 || !strings.HasPrefix(got.Message.Segments[1].URL, "data:image/png;base64,") || got.Tool.Result != "截图完成" {
		t.Fatalf("event = %#v", got)
	}
}

func TestManagerWorkersUseConfiguredProcessEnvironment(t *testing.T) {
	for _, mode := range []share.RuntimeMode{share.RuntimeModePersistent, share.RuntimeModeTransient} {
		t.Run(string(mode), func(t *testing.T) {
			baseEnv := processenv.New(os.Environ()).Fill(map[string]string{"ELBOT_WORKER_HOOK_ENV_TEST": "from-manager"})
			manager, err := NewManager(Options{Log: testLog, Notify: testNotify,
				SharedDir: t.TempDir(),
			})
			if err != nil {
				t.Fatal(err)
			}
			config := share.RuntimeConfig{
				Mode:                   mode,
				Command:                runtimeHelperCommand(),
				Cwd:                    ".",
				StartupTimeoutSeconds:  2,
				ShutdownTimeoutSeconds: 2,
				EventTimeoutSeconds:    2,
				MaxWaitSeconds:         30,
				Restart:                share.RuntimeRestartConfig{Strategy: "never", InitialDelaySeconds: 1, MaxDelaySeconds: 1},
				ID:                     "env",
				Dir:                    t.TempDir(),
				ProcessEnv:             baseEnv.Overlay(map[string]string{"ELBOT_WORKER_HOOK_ENV_TEST": "from-dotenv"}),
			}
			if err := manager.Apply([]share.RuntimeConfig{config}, func(string) {}); err != nil {
				t.Fatalf("Apply: %v", err)
			}
			t.Cleanup(func() { manager.Close(context.Background()) })
			if mode == share.RuntimeModePersistent {
				waitForStatus(t, manager, "env", StatusReady)
				return
			}
			event := share.Event{Platform: share.PlatformContext{Name: "test", ScopeID: "private:env"}, Actor: share.ActorContext{ID: "test:env", UserID: "env"}}
			if _, _, err := manager.Handle(context.Background(), "env", event, share.Control{}); err != nil {
				t.Fatalf("Handle: %v", err)
			}
		})
	}
}

type discardWriteCloser struct{}

func (discardWriteCloser) Write(data []byte) (int, error) { return len(data), nil }
func (discardWriteCloser) Close() error                   { return nil }

func TestWorkerRejectsOversizedInputFrame(t *testing.T) {
	w := &worker{stdin: discardWriteCloser{}}
	err := w.write(frame{Type: "request", Error: strings.Repeat("x", share.MaxProtocolFrameBytes)})
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
	manager, err := NewManager(Options{Log: testLog, Notify: testNotify, SharedDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	config := share.RuntimeConfig{
		Mode:                   share.RuntimeModePersistent,
		Command:                runtimeHelperCommand(),
		Cwd:                    ".",
		StartupTimeoutSeconds:  2,
		ShutdownTimeoutSeconds: 2,
		EventTimeoutSeconds:    2,
		MaxWaitSeconds:         30,
		Restart:                share.RuntimeRestartConfig{Strategy: "never", InitialDelaySeconds: 1, MaxDelaySeconds: 1},
		ID:                     "demo",
		Dir:                    t.TempDir(),
	}
	if err := manager.Apply([]share.RuntimeConfig{config}, func(string) {}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	t.Cleanup(func() { manager.Close(context.Background()) })
	waitForStatus(t, manager, "demo", StatusReady)
	event := share.Event{Platform: share.PlatformContext{Name: "test", ScopeID: "private:1"}, Actor: share.ActorContext{ID: "test:1", UserID: "1"}}
	observed, finished := 0, 0
	observe := func(ctx context.Context, name string) (context.Context, func()) {
		if name != "demo" {
			t.Fatalf("observed worker = %q", name)
		}
		observed++
		return ctx, func() { finished++ }
	}
	if _, routed, _, err := manager.Route(context.Background(), event, observe); err != nil || routed || observed != 0 {
		t.Fatalf("unmatched route registered a request: routed=%v observed=%d err=%v", routed, observed, err)
	}
	if _, _, err := manager.Handle(context.Background(), "demo", event, share.Control{Consume: true, StopPropagation: true}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	updated, handled, outcome, err := manager.Route(context.Background(), event, observe)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if observed != 1 || finished != 1 || outcome.ResultStatus != events.ResultSucceeded {
		t.Fatalf("continuation lifetime=%d/%d outcome=%+v", observed, finished, outcome)
	}
	if !handled || len(updated.Outputs) != 0 || updated.Control.Consume || updated.Control.StopPropagation {
		t.Fatalf("Route = handled=%v event=%#v", handled, updated)
	}
	updated, handled, _, err = manager.Route(context.Background(), event, observe)
	if err != nil {
		t.Fatalf("Route after completion: %v", err)
	}
	if handled || len(updated.Outputs) != 0 || observed != 1 || finished != 1 {
		t.Fatalf("Route after completion = handled=%v event=%#v", handled, updated)
	}
	if _, err := manager.Stop(context.Background(), "demo"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func TestManagerReclaimsTransientWorkerAfterPassThrough(t *testing.T) {
	manager, err := NewManager(Options{Log: testLog, Notify: testNotify, SharedDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	config := share.RuntimeConfig{
		Mode:                   share.RuntimeModeTransient,
		Command:                runtimeHelperCommand(),
		Cwd:                    ".",
		StartupTimeoutSeconds:  2,
		ShutdownTimeoutSeconds: 2,
		EventTimeoutSeconds:    2,
		MaxWaitSeconds:         30,
		Restart:                share.RuntimeRestartConfig{Strategy: "never", InitialDelaySeconds: 1, MaxDelaySeconds: 1},
		ID:                     "demo",
		Dir:                    t.TempDir(),
	}
	if err := manager.Apply([]share.RuntimeConfig{config}, func(string) {}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	t.Cleanup(func() { manager.Close(context.Background()) })
	event := share.Event{Platform: share.PlatformContext{Name: "test", ScopeID: "private:transient"}, Actor: share.ActorContext{ID: "test:transient", UserID: "transient"}}
	if _, _, err := manager.Handle(context.Background(), "demo", event, share.Control{Consume: true, StopPropagation: true}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	manager.mu.RLock()
	active := len(manager.transient)
	manager.mu.RUnlock()
	if active != 1 || !manager.hasLease(event) {
		t.Fatalf("transient worker = %d leased=%v, want one leased worker", active, manager.hasLease(event))
	}
	updated, routed, _, err := manager.Route(context.Background(), event, nil)
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
	manager, err := NewManager(Options{Log: testLog, Notify: testNotify, SharedDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	config := share.RuntimeConfig{
		Mode:                   share.RuntimeModeTransient,
		Command:                runtimeHelperCommand(),
		Cwd:                    ".",
		StartupTimeoutSeconds:  2,
		ShutdownTimeoutSeconds: 2,
		EventTimeoutSeconds:    2,
		MaxWaitSeconds:         30,
		Restart:                share.RuntimeRestartConfig{Strategy: "never", InitialDelaySeconds: 1, MaxDelaySeconds: 1},
		ID:                     "demo",
		Dir:                    t.TempDir(),
	}
	if err := manager.Apply([]share.RuntimeConfig{config}, func(string) {}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	t.Cleanup(func() { manager.Close(context.Background()) })
	event := share.Event{Platform: share.PlatformContext{Name: "test", ScopeID: "private:stop"}, Actor: share.ActorContext{ID: "test:stop", UserID: "stop"}}
	if _, _, err := manager.Handle(context.Background(), "demo", event, share.Control{}); err != nil {
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
	manager, err := NewManager(Options{Log: testLog, Notify: testNotify, SharedDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	config := share.RuntimeConfig{
		Mode:                   share.RuntimeModePersistent,
		Command:                runtimeHelperCommand(),
		Cwd:                    ".",
		StartupTimeoutSeconds:  2,
		ShutdownTimeoutSeconds: 2,
		EventTimeoutSeconds:    2,
		MaxWaitSeconds:         30,
		Restart:                share.RuntimeRestartConfig{Strategy: "never", InitialDelaySeconds: 1, MaxDelaySeconds: 1},
		ID:                     "demo",
		Dir:                    t.TempDir(),
	}
	if err := manager.Apply([]share.RuntimeConfig{config}, func(string) {}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	t.Cleanup(func() { manager.Close(context.Background()) })
	waitForStatus(t, manager, "demo", StatusReady)
	event := share.Event{Platform: share.PlatformContext{Name: "test", ScopeID: "private:defaults"}, Actor: share.ActorContext{ID: "test:defaults", UserID: "defaults"}}
	defaults := share.Control{Consume: true, StopPropagation: false}
	if _, _, err := manager.Handle(context.Background(), "demo", event, defaults); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	updated, routed, _, err := manager.Route(context.Background(), event, nil)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if !routed || !updated.Control.Consume || updated.Control.StopPropagation {
		t.Fatalf("Route = routed=%v control=%#v", routed, updated.Control)
	}
}

func TestManagerHandleSkipsReleasedWaitingOwner(t *testing.T) {
	manager, err := NewManager(Options{Log: testLog, Notify: testNotify, SharedDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	event := share.Event{Metadata: map[string]any{skipHookIDMetadataKey: "demo"}}
	updated, _, err := manager.Handle(context.Background(), "demo", event, share.Control{})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if updated.Metadata[skipHookIDMetadataKey] != "demo" {
		t.Fatalf("metadata = %#v", updated.Metadata)
	}
}

func TestManagerBlockedWaitingConversationFallsThrough(t *testing.T) {
	manager, err := NewManager(Options{Log: testLog, Notify: testNotify, SharedDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	config := share.RuntimeConfig{
		Mode:                   share.RuntimeModePersistent,
		Command:                runtimeHelperCommand(),
		Cwd:                    ".",
		StartupTimeoutSeconds:  2,
		ShutdownTimeoutSeconds: 2,
		EventTimeoutSeconds:    2,
		MaxWaitSeconds:         30,
		Restart:                share.RuntimeRestartConfig{Strategy: "never", InitialDelaySeconds: 1, MaxDelaySeconds: 1},
		ID:                     "demo",
		Dir:                    t.TempDir(),
	}
	if err := manager.Apply([]share.RuntimeConfig{config}, func(string) {}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	t.Cleanup(func() { manager.Close(context.Background()) })
	waitForStatus(t, manager, "demo", StatusReady)
	event := share.Event{Platform: share.PlatformContext{Name: "qqonebot", ScopeID: "group:123"}, Actor: share.ActorContext{ID: "qqonebot:1", UserID: "1"}}
	policy, err := share.NewBlockPolicy(nil, []string{"qqonebot:123"}, nil)
	if err != nil {
		t.Fatalf("NewBlockPolicy: %v", err)
	}
	manager.worker("demo").config.Block = policy
	if _, _, err := manager.Handle(context.Background(), "demo", event, share.Control{}); err != nil {
		t.Fatalf("blocked Handle: %v", err)
	}
	if manager.hasLease(event) {
		t.Fatal("blocked Handle created a waiting lease")
	}
	manager.worker("demo").config.Block = share.BlockPolicy{}
	if _, _, err := manager.Handle(context.Background(), "demo", event, share.Control{}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	manager.worker("demo").config.Block = policy
	updated, handled, _, err := manager.Route(context.Background(), event, nil)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if handled || len(updated.Outputs) != 0 || manager.hasLease(event) {
		t.Fatalf("blocked route = handled=%v event=%#v leased=%v", handled, updated, manager.hasLease(event))
	}
}

var runtimeHelperArgs = []string{"space arg", "", `C:\hook dir\`, `"quoted"`}

func TestWorkerRequestCancellationSendsEventCancelAndKeepsWorkerReady(t *testing.T) {
	manager, err := NewManager(Options{Log: testLog, Notify: testNotify, SharedDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	config := share.RuntimeConfig{Mode: share.RuntimeModePersistent, Command: runtimeHelperCommand(), Cwd: ".", StartupTimeoutSeconds: 2, ShutdownTimeoutSeconds: 2, EventTimeoutSeconds: 5, MaxWaitSeconds: 30, Restart: share.RuntimeRestartConfig{Strategy: "never", InitialDelaySeconds: 1, MaxDelaySeconds: 1}, ID: "cancel-aware", Dir: t.TempDir()}
	if err := manager.Apply([]share.RuntimeConfig{config}, func(string) {}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	t.Cleanup(func() { manager.Close(context.Background()) })
	waitForStatus(t, manager, config.ID, StatusReady)
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, _, err := manager.Handle(ctx, config.ID, share.Event{Platform: share.PlatformContext{ConversationID: "cancel-me"}, Actor: share.ActorContext{ID: "test:cancel"}}, share.Control{})
		errCh <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Handle error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Worker invocation did not cancel")
	}
	waitForStatus(t, manager, config.ID, StatusReady)
	if _, _, err := manager.Handle(context.Background(), config.ID, share.Event{Actor: share.ActorContext{ID: "test:defaults"}}, share.Control{}); err != nil {
		t.Fatalf("Worker was not reusable after cancellation: %v", err)
	}
}

func runtimeHelperCommand() []string {
	return append([]string{os.Args[0], "-test.run=TestHookRuntimeHelperProcess", "--", "hook-runtime-helper"}, runtimeHelperArgs...)
}

func waitForStatus(t *testing.T, manager *Manager, id string, wanted Status) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, info := range manager.List() {
			if info.ID == id && info.Status == string(wanted) {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("hook %q did not reach %s: %#v", id, wanted, manager.List())
}

func TestHookReloadRequestWaitsForActiveHandle(t *testing.T) {
	manager, err := NewManager(Options{Log: testLog, Notify: testNotify, SharedDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	committed := make(chan struct{})
	manager.SetPluginReloadPreparer(func(id string) (func() (events.LogResult, error), error) {
		if id != "demo" {
			t.Fatalf("reload id = %q, want demo", id)
		}
		return func() (events.LogResult, error) {
			close(committed)
			return events.LogResult{ResultStatus: events.ResultSucceeded, Content: "reload completed"}, nil
		}, nil
	})
	worker := newWorker(manager, share.RuntimeConfig{ID: "demo"})
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
	manager, err := NewManager(Options{Log: testLog, Notify: testNotify, SharedDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	config := func(id string) share.RuntimeConfig {
		return share.RuntimeConfig{
			Mode:                   share.RuntimeModePersistent,
			Command:                runtimeHelperCommand(),
			Cwd:                    ".",
			StartupTimeoutSeconds:  2,
			ShutdownTimeoutSeconds: 2,
			EventTimeoutSeconds:    2,
			MaxWaitSeconds:         30,
			Restart:                share.RuntimeRestartConfig{Strategy: "never", InitialDelaySeconds: 1, MaxDelaySeconds: 1},
			ID:                     id,
			Dir:                    t.TempDir(),
		}
	}
	demo := config("demo")
	other := config("other")
	if err := manager.Apply([]share.RuntimeConfig{demo, other}, func(string) {}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	t.Cleanup(func() { manager.Close(context.Background()) })
	waitForStatus(t, manager, "demo", StatusReady)
	waitForStatus(t, manager, "other", StatusReady)
	oldDemo := manager.worker("demo")
	oldOther := manager.worker("other")
	moved := demo
	moved.Dir = t.TempDir()
	if err := manager.ValidatePlugin(moved); err == nil {
		t.Fatal("expected self reload to reject a plugin path change")
	}
	if _, err := manager.ReplacePlugin(demo.ID, &demo, func() {}); err != nil {
		t.Fatalf("ReplacePlugin: %v", err)
	}
	if manager.worker("demo") == oldDemo {
		t.Fatal("demo worker was not replaced")
	}
	if manager.worker("other") != oldOther {
		t.Fatal("other worker was replaced")
	}
}

func TestManagerCloseRejectsNewStartsAndIsIdempotent(t *testing.T) {
	manager, err := NewManager(Options{Log: testLog, Notify: testNotify, SharedDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := manager.Close(context.Background()); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if err := manager.Apply(nil, func(string) {}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Apply after Close = %v, want ErrClosed", err)
	}
	if err := manager.Start("missing"); !errors.Is(err, ErrClosed) {
		t.Fatalf("Start after Close = %v, want ErrClosed", err)
	}
}

func TestManagerCloseCallerDeadlinePreservesWorkerShutdown(t *testing.T) {
	manager, err := NewManager(Options{Log: testLog, Notify: testNotify, SharedDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	config := share.RuntimeConfig{
		Mode:                   share.RuntimeModePersistent,
		Command:                runtimeHelperCommand(),
		Cwd:                    ".",
		StartupTimeoutSeconds:  2,
		ShutdownTimeoutSeconds: 2,
		EventTimeoutSeconds:    2,
		MaxWaitSeconds:         30,
		Restart:                share.RuntimeRestartConfig{Strategy: "never", InitialDelaySeconds: 1, MaxDelaySeconds: 1},
		ID:                     "slow-shutdown",
		Dir:                    t.TempDir(),
	}
	if err := manager.Apply([]share.RuntimeConfig{config}, func(string) {}); err != nil {
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
