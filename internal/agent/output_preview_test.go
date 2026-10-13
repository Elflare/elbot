package agent

import (
	"context"
	"errors"
	"slices"
	"testing"

	"elbot/internal/events"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/notification"
	"elbot/internal/signal"
)

func TestFormatToolPreviewPreservesMultilineBody(t *testing.T) {
	text := "shell 调用失败：exit status 1\n  stderr:\n    missing file\n\n后续说明"
	want := "[tool] " + text
	if got := formatToolPreview(text); got != want {
		t.Fatalf("preview = %q, want %q", got, want)
	}
}

type previewTestHooks struct {
	constructorHooks
	calls   int
	input   string
	point   hook.Point
	rewrite bool
	output  string
	err     error
}

func (h *previewTestHooks) Run(_ context.Context, e hook.Event) (hook.Event, events.LogResult, error) {
	h.calls++
	h.input = llm.SegmentsTextOnly(e.Message.Segments)
	h.point = e.Point
	if h.rewrite {
		e.Message.Segments = llm.TextSegments(h.output)
	}
	return e, events.LogResult{ResultStatus: events.ResultSkipped}, h.err
}

func TestPreviewGroupsToolNoticesBeforeHook(t *testing.T) {
	tests := []struct {
		name    string
		text    []string
		input   string
		want    []string
		rewrite bool
		output  string
		err     error
	}{
		{
			name:  "single line",
			text:  []string{"正在调用 shell：{}"},
			input: "[tool] 正在调用 shell：{}",
			want:  []string{"[tool] 正在调用 shell：{}"},
		},
		{
			name:  "multiline error",
			text:  []string{"shell 调用失败：exit status 1\n  stderr:\n    missing file\n\n后续说明"},
			input: "[tool] shell 调用失败：exit status 1\n  stderr:\n    missing file\n\n后续说明",
			want:  []string{"[tool] shell 调用失败：exit status 1\n  stderr:\n    missing file\n\n后续说明"},
		},
		{
			name:  "batch with blank arguments",
			text:  []string{" ", " 正在调用 shell：{}\n  参数说明 ", "", "正在调用 read_file：{}", "\n"},
			input: "[tool] 正在调用 shell：{}\n  参数说明\n[tool] 正在调用 read_file：{}",
			want:  []string{"[tool] 正在调用 shell：{}\n  参数说明\n[tool] 正在调用 read_file：{}"},
		},
		{name: "no arguments"},
		{name: "all blank", text: []string{"", " \n\t "}},
		{
			name:    "hook replaces prefixes",
			text:    []string{"shell 提示", "read_file 提示"},
			input:   "[tool] shell 提示\n[tool] read_file 提示",
			rewrite: true,
			output:  "Hook 修改后的正文\n  保留缩进",
			want:    []string{"Hook 修改后的正文\n  保留缩进"},
		},
		{
			name:    "hook clears body",
			text:    []string{"shell 提示"},
			input:   "[tool] shell 提示",
			rewrite: true,
			output:  " \n ",
		},
		{
			name:  "hook fails",
			text:  []string{"shell 提示"},
			input: "[tool] shell 提示",
			err:   errors.New("output hook failed"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager, err := notification.New("cli")
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close(context.Background())
			platformSignal, err := manager.AddPlatform("cli")
			if err != nil {
				t.Fatal(err)
			}
			var seen []string
			_, err = platformSignal.Connect(func(_ context.Context, n events.Notification) (events.NotificationReceipt, error) {
				if n.Text != "preview test barrier" {
					seen = append(seen, n.Text)
				}
				return events.NotificationReceipt{}, nil
			}, signal.ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			hooks := &previewTestHooks{rewrite: tt.rewrite, output: tt.output, err: tt.err}
			out := &OutputSender{hooks: hooks}
			out.Preview(context.Background(), tt.text...)
			barrier, err := events.NotificationRequested.EmitAsync(context.Background(), events.Notification{Text: "preview test barrier"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := barrier.Wait(context.Background()); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(seen, tt.want) {
				t.Fatalf("notifications = %q, want %q", seen, tt.want)
			}
			wantCalls := 1
			if tt.input == "" {
				wantCalls = 0
			}
			if hooks.calls != wantCalls || hooks.input != tt.input {
				t.Fatalf("Hook calls/input = %d/%q, want %d/%q", hooks.calls, hooks.input, wantCalls, tt.input)
			}
			if hooks.calls != 0 && hooks.point != hook.PointAgentOutputPrepared {
				t.Fatalf("Hook point = %s, want %s", hooks.point, hook.PointAgentOutputPrepared)
			}
		})
	}
}
