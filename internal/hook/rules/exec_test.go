package rules

import (
	"bufio"
	"bytes"
	"context"
	"elbot/internal/delivery"
	"elbot/internal/hook"
	hookruntime "elbot/internal/hook/runtime"
	"elbot/internal/llm"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestExecActionDefaultStdinIncludesEvent(t *testing.T) {
	module := Module{}
	event := hook.Event{Point: hook.PointAgentInputPrepared, Actor: hook.ActorContext{UserID: "alice"}, Message: hook.MessagePayload{PlatformMessage: []byte(`[{"type":"json"}]`)}}
	got, err := module.runRule(context.Background(), Rule{Actions: []Action{{Type: "exec", Command: execHelperCommand("stdin")}}}, event)
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	if len(got.Outputs) != 1 || !strings.Contains(got.Outputs[0].Text, `"type":"request"`) || !strings.Contains(got.Outputs[0].Text, `"method":"event.handle"`) || !strings.Contains(got.Outputs[0].Text, `"user_id":"alice"`) || !strings.Contains(got.Outputs[0].Text, `"platform_message":[{"type":"json"}]`) {
		t.Fatalf("outputs = %#v", got.Outputs)
	}
}

func TestExecCommandPreservesArgvAndRendersEachArgument(t *testing.T) {
	event := hook.Event{Message: hook.MessagePayload{Segments: []llm.MessageSegment{{Type: llm.SegmentText, Text: "上海 天气"}}}}
	command := execHelperCommand("argv", "{{message.text}}", "", `C:\hook dir\`, `"quoted"`)
	got, err := (Module{}).runRule(context.Background(), Rule{Actions: []Action{{Type: "exec", Command: command}}}, event)
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
	runtimeManager := hookruntime.NewManager(hookruntime.Options{})
	defer runtimeManager.Close(context.Background())
	if err := runtimeManager.SharedState().SetWithTTL("worker-data", json.RawMessage(`"from-worker"`), 0); err != nil {
		t.Fatalf("seed shared state: %v", err)
	}
	module := Module{Opts: Options{Runtime: runtimeManager}}
	if _, err := module.runRule(context.Background(), Rule{Actions: []Action{{
		Type:    "exec",
		Command: execHelperCommand("shared-state"),
	}}}, hook.Event{Point: hook.PointLLMResponseReceived}); err != nil {
		t.Fatalf("run exec shared state: %v", err)
	}
	value, ok := runtimeManager.SharedState().Get("once-data")
	if !ok || string(value) != "2" {
		t.Fatalf("once-data = %s, %v", value, ok)
	}
}

func TestExecSharedStateRequiresRuntime(t *testing.T) {
	params := json.RawMessage(`{"key":"missing"}`)
	if _, err := (Module{}).handleProtocolRequest(context.Background(), hook.Event{}, Action{}, state{}, "shared.get", params); err == nil || !strings.Contains(err.Error(), "runtime is not configured") {
		t.Fatalf("shared request error = %v", err)
	}
}

func TestExecOutputFrameLargerThanScannerDefaultSucceeds(t *testing.T) {
	module := Module{}
	const decodedSize = 70 * 1024
	got, err := module.runRule(context.Background(), Rule{Actions: []Action{{Type: "exec", Command: execHelperCommand("base64-output", fmt.Sprint(decodedSize))}}}, hook.Event{})
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

func TestExecProcessErrorPreservesContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := execProcessError(ctx, Action{}, fmt.Errorf("signal: killed"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("execProcessError = %v, want context canceled", err)
	}
	if !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("execProcessError text = %q, want canceled", err.Error())
	}
}

func TestExecDoneMessageWritesConfiguredFieldAndResult(t *testing.T) {
	module := Module{}
	event := hook.Event{Point: hook.PointLLMResponseReceived, LLM: hook.LLMPayload{Text: "old"}}
	got, err := module.runRule(context.Background(), Rule{Actions: []Action{
		{ActionName: "script", Type: "exec", Command: execHelperCommand("done-message"), Field: "llm.text"},
		{Type: "send", Text: "{{actions.script.result}}"},
	}}, event)
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
	module := Module{}
	event := hook.Event{Point: hook.PointLLMResponseReceived, LLM: hook.LLMPayload{Text: "[[atri_emotions]]"}}
	got, err := module.runRule(context.Background(), Rule{Actions: []Action{
		{Type: "exec", Command: execHelperCommand("done-empty-message"), Field: "llm.text"},
	}}, event)
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	if got.LLM.Text != "" {
		t.Fatalf("llm.text = %q, want empty", got.LLM.Text)
	}
}

func TestExecDoneSegmentsReplacesToolResultContent(t *testing.T) {
	module := Module{}
	event := hook.Event{
		Point:   hook.PointToolCallCompleted,
		Message: hook.MessagePayload{Role: string(llm.RoleTool), Segments: llm.TextSegments("old")},
		Tool:    hook.ToolPayload{Name: "screenshot", Result: "old"},
	}
	got, err := module.runRule(context.Background(), Rule{Actions: []Action{
		{Type: "exec", Command: execHelperCommand("done-segments"), Field: "tool.result"},
	}}, event)
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	if len(got.Message.Segments) != 2 || got.Message.Segments[1].Type != llm.SegmentImage || !strings.HasPrefix(got.Message.Segments[1].URL, "data:image/png;base64,") {
		t.Fatalf("segments = %#v", got.Message.Segments)
	}
	if got.Tool.Result != "截图完成" {
		t.Fatalf("tool.result = %q", got.Tool.Result)
	}
}

func TestExecDoneUnmatchedRollsBackAndSkipsRemainingActions(t *testing.T) {
	module := Module{}
	event := hook.Event{Point: hook.PointLLMResponseReceived, LLM: hook.LLMPayload{Text: "old"}}
	got, err := module.runRule(context.Background(), Rule{Actions: []Action{
		{Type: "exec", Command: execHelperCommand("unmatched"), Field: "llm.text"},
		{Type: "send", Text: "after"},
	}}, event)
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

func TestExecSuccessLogsStderrWithoutReadFailure(t *testing.T) {
	var logs bytes.Buffer
	module := Module{Logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	_, err := module.runRule(context.Background(), Rule{Actions: []Action{{
		ActionName: "script",
		Type:       "exec",
		Command:    execHelperCommand("stderr-success"),
	}}}, hook.Event{Point: hook.PointLLMResponseReceived})
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	gotLogs := logs.String()
	if !strings.Contains(gotLogs, "hook exec stderr") || !strings.Contains(gotLogs, "plugin diagnostic") {
		t.Fatalf("logs missing stderr line:\n%s", gotLogs)
	}
	if strings.Contains(gotLogs, "read failed") || strings.Contains(gotLogs, "file already closed") {
		t.Fatalf("stderr logging reported internal read failure:\n%s", gotLogs)
	}
}

func TestExecFlushesStderrWithoutTrailingNewline(t *testing.T) {
	var logs bytes.Buffer
	module := Module{Logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	_, err := module.runRule(context.Background(), Rule{Actions: []Action{{
		Type:    "exec",
		Command: execHelperCommand("stderr-no-newline"),
	}}}, hook.Event{Point: hook.PointLLMResponseReceived})
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
		wantControl hook.Control
	}{
		{name: "true allows propagation", mode: "pass-true", consume: true, stop: true, wantControl: hook.Control{}},
		{name: "false blocks propagation", mode: "pass-false", wantControl: hook.Control{Consume: true, StopPropagation: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := (Module{}).runRule(context.Background(), Rule{Name: "pass", Consume: tc.consume, StopPropagation: tc.stop, Actions: []Action{{Type: "exec", Command: execHelperCommand(tc.mode)}}}, hook.Event{})
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
	module := Module{}
	got, err := module.runRule(context.Background(), Rule{Actions: []Action{
		{ActionName: "first", Type: "exec", Command: execHelperCommand("done-result", "one")},
		{Type: "send", Text: "{{actions.first.result}}"},
	}}, hook.Event{Point: hook.PointLLMResponseReceived})
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	if len(got.Outputs) != 1 || got.Outputs[0].Text != "one" {
		t.Fatalf("outputs = %#v, want result from completed first exec", got.Outputs)
	}
}

func TestExecRunsDoNotShareGlobalBlockingLock(t *testing.T) {
	dir := t.TempDir()
	left := filepath.Join(dir, "left.ready")
	right := filepath.Join(dir, "right.ready")
	run := func(self, peer string) error {
		_, err := Module{}.runRule(context.Background(), Rule{Actions: []Action{{
			Type:           "exec",
			Command:        execHelperCommand("signal-and-wait", self, peer),
			TimeoutSeconds: 4,
		}}}, hook.Event{Point: hook.PointLLMResponseReceived})
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
			_, err := Module{}.runRule(context.Background(), Rule{Actions: []Action{{
				Type:           "exec",
				Command:        execHelperCommand(tt.helper),
				TimeoutSeconds: tt.timeoutSeconds,
			}}}, hook.Event{Point: hook.PointLLMResponseReceived})
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
			_, err := Module{}.runRule(context.Background(), Rule{Actions: []Action{{
				Type:    "exec",
				Command: execHelperCommand(tt.helper),
			}}}, hook.Event{Point: hook.PointLLMResponseReceived})
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
	_, err := Module{}.runRule(context.Background(), Rule{Actions: []Action{{
		Type:           "exec",
		Command:        execHelperCommand("close-stdin-after-request"),
		TimeoutSeconds: 2,
	}}}, hook.Event{Point: hook.PointLLMResponseReceived})
	if err == nil {
		t.Fatal("expected exec error")
	}
	for _, want := range []string{"write hook plugin stdin response frame failed", "closed stdin"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want %q", err.Error(), want)
		}
	}
}
