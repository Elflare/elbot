package output

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/notification"
	"elbot/internal/signal"
	"errors"
	"slices"
	"testing"
)

func TestFormatToolPreviewPreservesMultilineBody(t *testing.T) {
	text := "shell 调用失败：exit status 1\n  stderr:\n    missing file\n\n后续说明"
	want := "[tool] " + text
	if got := formatToolPreview(text); got != want {
		t.Fatalf("preview = %q, want %q", got, want)
	}
}

func TestPreviewConversationPolicyPrecedesHookAndSignal(t *testing.T) {
	for _, platform := range []string{"qqonebot", "qqofficial", "telegram", "cli"} {
		for _, kind := range []contextinfo.ConversationKind{contextinfo.ConversationPrivate, contextinfo.ConversationGroup, contextinfo.ConversationChannel, contextinfo.ConversationUnknown} {
			for _, batch := range []bool{false, true} {
				t.Run(platform+"/"+string(kind)+"/"+map[bool]string{false: "single", true: "batch"}[batch], func(t *testing.T) {
					var seen []string
					connection, err := events.NotificationRequested.Observe(func(_ context.Context, notice events.Notification) error {
						if notice.Text != "barrier" {
							seen = append(seen, notice.Text)
						}
						return nil
					}, signal.ConnectOptions{})
					if err != nil {
						t.Fatal(err)
					}
					defer connection.Disconnect()
					hooks := &previewTestHooks{rewrite: true, output: "rewritten without tool prefix"}
					out := &OutputSender{hooks: hooks}
					ctx := contextinfo.WithConversation(t.Context(), contextinfo.Conversation{Source: contextinfo.Source{Platform: platform, ConversationKind: kind}})
					text := []string{"正在调用 first"}
					if batch {
						text = append(text, "正在调用 second")
					}
					out.Preview(ctx, text...)
					if _, err := events.NotificationRequested.Emit(ctx, events.Notification{Text: "barrier"}); err != nil {
						t.Fatal(err)
					}
					want := 1
					if kind == contextinfo.ConversationGroup {
						want = 0
					}
					if hooks.calls != want || len(seen) != want {
						t.Fatalf("hooks=%d notices=%v", hooks.calls, seen)
					}
					if want == 1 && seen[0] != hooks.output {
						t.Fatalf("notice=%q", seen[0])
					}
				})
			}
		}
	}
}

type previewTestHooks struct {
	calls   int
	input   string
	rewrite bool
	output  string
	err     error
}

func (*previewTestHooks) AgentTurnOutputPrepared(_ context.Context, text string) (string, events.LogResult, error) {
	return text, events.LogResult{}, nil
}

func (*previewTestHooks) PlatformMessageSent(context.Context, string) {}

// Both construction and storage accept only the three output callbacks.
var (
	_ = OutputSenderDeps{Hooks: &previewTestHooks{}}
	_ = OutputSender{hooks: &previewTestHooks{}}
)

func (h *previewTestHooks) AgentOutputPrepared(_ context.Context, text string) (string, events.LogResult, error) {
	h.calls++
	h.input = text
	if h.rewrite {
		text = h.output
	}
	return text, events.LogResult{ResultStatus: events.ResultSkipped}, h.err
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
			want:  []string{"对话处理失败：准备工具预览: output hook failed"},
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
		})
	}
}
