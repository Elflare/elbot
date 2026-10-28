// 验证一次性插件的参数、环境、协议和执行结果。

package rules

import (
	"bufio"
	"bytes"
	"context"
	"elbot/internal/processenv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
	hookruntime "elbot/internal/hook/runtime"
	"elbot/internal/hook/share"
	"elbot/internal/llm"
	"elbot/internal/signal"
	"elbot/internal/testutil"
	"elbot/internal/testutil/mediatest"
)

func TestExecActionDefaultStdinIncludesEvent(t *testing.T) {
	module := Module{Opts: Options{Log: testLog, Notify: testNotify}}
	event := share.Event{Point: share.PointAgentInputPrepared, Actor: share.ActorContext{UserID: "alice"}, Message: share.MessagePayload{PlatformMessage: []byte(`[{"type":"json"}]`)}}
	got, _, err := module.runRule(context.Background(), Rule{Actions: []Action{{Type: "exec", Command: execHelperCommand("stdin")}}}, event, false)
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	if len(got.Outputs) != 1 || !strings.Contains(got.Outputs[0].Text, `"type":"request"`) || !strings.Contains(got.Outputs[0].Text, `"method":"event.handle"`) || !strings.Contains(got.Outputs[0].Text, `"user_id":"alice"`) || !strings.Contains(got.Outputs[0].Text, `"platform_message":[{"type":"json"}]`) {
		t.Fatalf("outputs = %#v", got.Outputs)
	}
}

func TestExecCommandPreservesArgvAndRendersEachArgument(t *testing.T) {
	event := share.Event{Message: share.MessagePayload{Segments: []llm.MessageSegment{{Type: llm.SegmentText, Text: "上海 天气"}}}}
	command := execHelperCommand("argv", "{{message.text}}", "", `C:\hook dir\`, `"quoted"`)
	got, _, err := (Module{Opts: Options{Log: testLog, Notify: testNotify}}).runRule(context.Background(), Rule{Actions: []Action{{Type: "exec", Command: command}}}, event, false)
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	if len(got.Outputs) != 1 {
		t.Fatalf("outputs = %#v", got.Outputs)
	}
	var args []string
	if err := json.Unmarshal([]byte(got.Outputs[0].Text), &args); err != nil {
		t.Fatalf("decode argv: %v", err)
	}
	want := []string{"上海 天气", "", `C:\hook dir\`, `"quoted"`}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("argv = %#v, want %#v", args, want)
	}
}

func TestExecCommandUsesConfiguredProcessEnvironment(t *testing.T) {
	const key = "ELBOT_EXEC_HOOK_ENV_TEST"
	baseEnv := processenv.New(os.Environ()).Fill(map[string]string{key: "from-manager"})
	module := Module{Opts: Options{Log: testLog, Notify: testNotify, ProcessEnv: baseEnv}}
	rule := Rule{
		Actions: []Action{{Type: "exec", Command: execHelperCommand("env", key)}},
		source:  ruleSource{ProcessEnv: baseEnv.Overlay(map[string]string{key: "from-dotenv"})},
	}
	cfg := Config{Rules: []Rule{rule}}
	if err := normalizeLoadedRules(&cfg, Options{Log: testLog, Notify: testNotify}); err != nil {
		t.Fatal(err)
	}
	got, _, err := module.runRule(context.Background(), cfg.Rules[0], share.Event{}, false)
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	if len(got.Outputs) != 1 || got.Outputs[0].Text != "from-dotenv" {
		t.Fatalf("outputs = %#v", got.Outputs)
	}
}

func TestWriteProtocolFrameRejectsOversizedInput(t *testing.T) {
	var out bytes.Buffer
	err := writeProtocolFrame(&out, map[string]any{"data": strings.Repeat("x", maxHookProtocolFrameBytes)})
	if err == nil || !strings.Contains(err.Error(), "stdin frame exceeds 16 MiB limit") {
		t.Fatalf("err = %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("wrote %d bytes", out.Len())
	}
}

func TestExecCanUseRuntimeSharedState(t *testing.T) {
	runtimeManager, err := hookruntime.NewManager(hookruntime.Options{Log: testLog, Notify: testNotify, SharedDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer runtimeManager.Close(context.Background())
	if _, err := runtimeManager.SharedRequest("shared.set", json.RawMessage(`{"key":"worker-data","value":"from-worker","ttl_seconds":0}`)); err != nil {
		t.Fatalf("seed shared state: %v", err)
	}
	module := Module{Opts: Options{Log: testLog, Notify: testNotify, Runtime: runtimeManager}}
	if _, _, err := module.runRule(context.Background(), Rule{Actions: []Action{{
		Type:    "exec",
		Command: execHelperCommand("shared-state"),
	}}}, share.Event{Point: share.PointLLMResponseReceived}, false); err != nil {
		t.Fatalf("run exec shared state: %v", err)
	}
	value, err := runtimeManager.SharedRequest("shared.get", json.RawMessage(`{"key":"once-data"}`))
	if err != nil {
		t.Fatal(err)
	}
	result := value.(map[string]any)
	if result["found"] != true || string(result["value"].(json.RawMessage)) != "2" {
		t.Fatalf("once-data = %#v", result)
	}
}

func TestExecOutputFrameLargerThanScannerDefaultSucceeds(t *testing.T) {
	module := Module{Opts: Options{Log: testLog, Notify: testNotify}}
	const decodedSize = 70 * 1024
	got, _, err := module.runRule(context.Background(), Rule{Actions: []Action{{Type: "exec", Command: execHelperCommand("base64-output", fmt.Sprint(decodedSize))}}}, share.Event{}, false)
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	if len(got.Outputs) != 1 {
		t.Fatalf("outputs = %#v", got.Outputs)
	}
	if got.Outputs[0].Kind != delivery.KindImage || len(got.Outputs[0].Source.Data) != decodedSize {
		t.Fatalf("output = %#v, data len %d", got.Outputs[0], len(got.Outputs[0].Source.Data))
	}
}

func TestReadProtocolLineRejectsOversizedFrame(t *testing.T) {
	_, err := readProtocolLine(bufio.NewReader(strings.NewReader(strings.Repeat("x", maxHookProtocolFrameBytes+1))))
	if err == nil || !strings.Contains(err.Error(), "stdout frame exceeds 16 MiB limit") || !strings.Contains(err.Error(), "outputs[].path") {
		t.Fatalf("err = %v", err)
	}
}

func TestExecCancellationKillsDescendantProcesses(t *testing.T) {
	const waitLimit = 10 * time.Second
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := listener.SetDeadline(time.Now().Add(waitLimit)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		_, _, err := (Module{Opts: Options{Log: testLog, Notify: testNotify}}).runRule(ctx, Rule{Actions: []Action{{Type: "exec", Command: execHelperCommand("spawn-child-and-wait", listener.Addr().String())}}}, share.Event{}, false)
		errCh <- err
	}()
	conn, err := listener.AcceptTCP()
	if err != nil {
		t.Fatalf("child process did not connect: %v", err)
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(waitLimit)); err != nil {
		t.Fatal(err)
	}
	var ready [5]byte
	if _, err := io.ReadFull(conn, ready[:]); err != nil || string(ready[:]) != "ready" {
		t.Fatalf("child process did not become ready: %q, %v", ready, err)
	}
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("runRule error = %v, want context canceled", err)
		}
	case <-time.After(waitLimit):
		t.Fatal("Hook did not stop after cancellation")
	}
	// The child never closes voluntarily; EOF or a reset proves it was terminated.
	if err := conn.SetReadDeadline(time.Now().Add(waitLimit)); err != nil {
		t.Fatal(err)
	}
	var probe [1]byte
	n, err := conn.Read(probe[:])
	var netErr net.Error
	if n != 0 || err == nil || (errors.As(err, &netErr) && netErr.Timeout()) {
		t.Fatalf("descendant survived cancellation: read %d bytes, error %v", n, err)
	}
}

func TestExecDoneMessageWritesConfiguredFieldAndResult(t *testing.T) {
	module := Module{Opts: Options{Log: testLog, Notify: testNotify}}
	event := share.Event{Point: share.PointLLMResponseReceived, LLM: share.LLMPayload{Text: "old"}}
	got, _, err := module.runRule(context.Background(), Rule{Actions: []Action{
		{ActionName: "script", Type: "exec", Command: execHelperCommand("done-message"), Field: "llm.text"},
		{Type: "send", Text: "{{actions.script.result}}"},
	}}, event, false)
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	if got.LLM.Text != "clean" {
		t.Fatalf("llm.text = %q, want clean", got.LLM.Text)
	}
	if len(got.Outputs) != 1 || got.Outputs[0].Text != "ok" {
		t.Fatalf("outputs = %#v", got.Outputs)
	}
}

func TestExecDoneEmptyMessageClearsConfiguredField(t *testing.T) {
	module := Module{Opts: Options{Log: testLog, Notify: testNotify}}
	event := share.Event{Point: share.PointLLMResponseReceived, LLM: share.LLMPayload{Text: "[[atri_emotions]]"}}
	got, _, err := module.runRule(context.Background(), Rule{Actions: []Action{
		{Type: "exec", Command: execHelperCommand("done-empty-message"), Field: "llm.text"},
	}}, event, false)
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	if got.LLM.Text != "" {
		t.Fatalf("llm.text = %q, want empty", got.LLM.Text)
	}
}

func TestExecDoneSegmentsReplacesToolResultContent(t *testing.T) {
	runtime, err := hookruntime.NewManager(hookruntime.Options{Log: testLog, Notify: testNotify, SharedDir: t.TempDir(), Media: mediatest.New(t, testutil.NewSQLiteStore(t), t.TempDir())})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	module := Module{Opts: Options{Log: testLog, Notify: testNotify, BuildMessageSegments: runtime.BuildMessageSegments}}
	event := share.Event{
		Point:   share.PointToolCallCompleted,
		Message: share.MessagePayload{Role: string(llm.RoleTool), Segments: llm.TextSegments("old")},
		Tool:    share.ToolPayload{Name: "screenshot", Result: "old"},
	}
	got, _, err := module.runRule(context.Background(), Rule{Actions: []Action{
		{Type: "exec", Command: execHelperCommand("done-segments"), Field: "tool.result"},
	}}, event, false)
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	if len(got.Message.Segments) != 2 || got.Message.Segments[1].Type != llm.SegmentImage || got.Message.Segments[1].MediaID == "" || got.Message.Segments[1].URL != "" {
		t.Fatalf("segments = %#v", got.Message.Segments)
	}
	if got.Tool.Result != "截图完成" {
		t.Fatalf("tool.result = %q", got.Tool.Result)
	}
}

func TestExecDoneUnmatchedRollsBackAndSkipsRemainingActions(t *testing.T) {
	module := Module{Opts: Options{Log: testLog, Notify: testNotify}}
	event := share.Event{Point: share.PointLLMResponseReceived, LLM: share.LLMPayload{Text: "old"}}
	got, _, err := module.runRule(context.Background(), Rule{Actions: []Action{
		{Type: "exec", Command: execHelperCommand("unmatched"), Field: "llm.text"},
		{Type: "send", Text: "after"},
	}}, event, false)
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	if got.LLM.Text != "old" {
		t.Fatalf("llm.text = %q, want old", got.LLM.Text)
	}
	if len(got.Outputs) != 0 {
		t.Fatalf("outputs = %#v", got.Outputs)
	}
}

func TestExecSuccessLogsStderr(t *testing.T) {
	var logs bytes.Buffer
	captureLogs(t, slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	module := Module{Opts: Options{Log: testLog, Notify: testNotify}}
	_, _, err := module.runRule(context.Background(), Rule{Actions: []Action{{
		ActionName: "script",
		Type:       "exec",
		Command:    execHelperCommand("stderr-success"),
	}}}, share.Event{Point: share.PointLLMResponseReceived}, false)
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	gotLogs := logs.String()
	if !strings.Contains(gotLogs, "hook exec stderr") || !strings.Contains(gotLogs, "plugin diagnostic") {
		t.Fatalf("logs missing stderr line:\n%s", gotLogs)
	}
}

func TestExecFlushesStderrWithoutTrailingNewline(t *testing.T) {
	var logs bytes.Buffer
	captureLogs(t, slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	module := Module{Opts: Options{Log: testLog, Notify: testNotify}}
	_, _, err := module.runRule(context.Background(), Rule{Actions: []Action{{
		Type:    "exec",
		Command: execHelperCommand("stderr-no-newline"),
	}}}, share.Event{Point: share.PointLLMResponseReceived}, false)
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	if gotLogs := logs.String(); !strings.Contains(gotLogs, "partial diagnostic") {
		t.Fatalf("logs missing partial stderr line:\n%s", gotLogs)
	}
}

func TestExecPassThroughOverridesRuleControl(t *testing.T) {
	tests := []struct {
		name        string
		mode        string
		consume     bool
		stop        bool
		wantControl share.Control
	}{
		{name: "true allows propagation", mode: "pass-true", consume: true, stop: true, wantControl: share.Control{}},
		{name: "false blocks propagation", mode: "pass-false", wantControl: share.Control{Consume: true, StopPropagation: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := (Module{Opts: Options{Log: testLog, Notify: testNotify}}).runRule(context.Background(), Rule{Name: "pass", Consume: tc.consume, StopPropagation: tc.stop, Actions: []Action{{Type: "exec", Command: execHelperCommand(tc.mode)}}}, share.Event{}, false)
			if err != nil {
				t.Fatal(err)
			}
			if got.Control != tc.wantControl {
				t.Fatalf("control = %#v, want %#v", got.Control, tc.wantControl)
			}
		})
	}
}

func TestExecActionsRunSynchronouslyInOrder(t *testing.T) {
	module := Module{Opts: Options{Log: testLog, Notify: testNotify}}
	got, _, err := module.runRule(context.Background(), Rule{Actions: []Action{
		{ActionName: "first", Type: "exec", Command: execHelperCommand("done-result", "one")},
		{Type: "send", Text: "{{actions.first.result}}"},
	}}, share.Event{Point: share.PointLLMResponseReceived}, false)
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	if len(got.Outputs) != 1 || got.Outputs[0].Text != "one" {
		t.Fatalf("outputs = %#v, want result from completed first exec", got.Outputs)
	}
}

func TestExecActionsRunConcurrently(t *testing.T) {
	dir := t.TempDir()
	left := filepath.Join(dir, "left.ready")
	right := filepath.Join(dir, "right.ready")
	run := func(self, peer string) error {
		_, _, err := Module{Opts: Options{Log: testLog, Notify: testNotify}}.runRule(context.Background(), Rule{Actions: []Action{{
			Type:           "exec",
			Command:        execHelperCommand("signal-and-wait", self, peer),
			TimeoutSeconds: 4,
		}}}, share.Event{Point: share.PointLLMResponseReceived}, false)
		return err
	}
	errCh := make(chan error, 2)
	go func() { errCh <- run(left, right) }()
	go func() { errCh <- run(right, left) }()
	for i := 0; i < 2; i++ {
		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("parallel exec run failed: %v", err)
			}
		case <-time.After(6 * time.Second):
			t.Fatal("parallel exec runs blocked each other")
		}
	}
}

// captureLogs observes the public signal; production never receives this logger.
func captureLogs(t *testing.T, logger *slog.Logger) {
	t.Helper()
	connection, err := globalevents.LogSubmitted.Observe(func(ctx context.Context, record globalevents.LogRecord) error {
		if !logger.Enabled(ctx, record.Level) {
			return nil
		}
		out := slog.NewRecord(time.Now(), record.Level, record.Content, 0)
		out.AddAttrs(slog.String("event", record.EventName), slog.String("module", record.Module))
		return logger.Handler().Handle(ctx, out)
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(connection.Disconnect)
}

func TestExecFailuresIncludeStderrTail(t *testing.T) {
	tests := []struct {
		name           string
		helper         string
		timeoutSeconds int
		want           []string
	}{
		{
			name:   "nonzero exit",
			helper: "crash-stderr",
			want:   []string{"closed stdout", "stderr:", "script exploded"},
		},
		{
			name:   "closed before event response",
			helper: "missing-done-stderr",
			want:   []string{"closed stdout", "stderr:", "wrote stderr before clean exit"},
		},
		{
			name:   "invalid json",
			helper: "invalid-json-stderr",
			want:   []string{"parse hook.v2 stdout frame", "stderr:", "bad json stderr"},
		},
		{
			name:           "timeout",
			helper:         "sleep-stderr",
			timeoutSeconds: 1,
			want:           []string{"exec timed out after 1s", "stderr:", "waiting forever"},
		},
		{
			name:   "stderr without trailing newline",
			helper: "stderr-no-newline-crash",
			want:   []string{"closed stdout", "stderr:", "partial crash diagnostic"},
		},
		{
			name:   "many stderr lines keeps tail",
			helper: "many-stderr",
			want:   []string{"closed stdout", "stderr:", "earlier stderr lines omitted", "stderr line 24"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := Module{Opts: Options{Log: testLog, Notify: testNotify}}.runRule(context.Background(), Rule{Actions: []Action{{
				Type:           "exec",
				Command:        execHelperCommand(tt.helper),
				TimeoutSeconds: tt.timeoutSeconds,
			}}}, share.Event{Point: share.PointLLMResponseReceived}, false)
			if err == nil {
				t.Fatal("expected exec error")
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error = %q, want %q", err.Error(), want)
				}
			}
		})
	}
}

func TestExecProtocolErrorsIdentifyPluginProblem(t *testing.T) {
	tests := []struct {
		name   string
		helper string
		want   []string
	}{
		{
			name:   "unknown frame",
			helper: "unknown-frame",
			want:   []string{"unsupported hook.v2 frame type", "mystery"},
		},
		{
			name:   "bad output field",
			helper: "bad-output",
			want:   []string{"unsupported hook.v2 frame type", "output"},
		},
		{
			name:   "plugin error frame",
			helper: "plugin-error-frame",
			want:   []string{"unsupported hook.v2 frame type", "error"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := Module{Opts: Options{Log: testLog, Notify: testNotify}}.runRule(context.Background(), Rule{Actions: []Action{{
				Type:    "exec",
				Command: execHelperCommand(tt.helper),
			}}}, share.Event{Point: share.PointLLMResponseReceived}, false)
			if err == nil {
				t.Fatal("expected exec error")
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error = %q, want %q", err.Error(), want)
				}
			}
		})
	}
}

func TestExecResponseWriteFailureIdentifiesPluginStdinProblem(t *testing.T) {
	_, _, err := Module{Opts: Options{Log: testLog, Notify: testNotify}}.runRule(context.Background(), Rule{Actions: []Action{{
		Type:           "exec",
		Command:        execHelperCommand("close-stdin-after-request"),
		TimeoutSeconds: 2,
	}}}, share.Event{Point: share.PointLLMResponseReceived}, false)
	if err == nil {
		t.Fatal("expected exec error")
	}
	for _, want := range []string{"write hook plugin stdin response frame failed", "closed stdin"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want %q", err.Error(), want)
		}
	}
}
