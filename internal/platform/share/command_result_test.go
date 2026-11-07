package share

import (
	"strings"
	"testing"
	"time"

	command "elbot/internal/command/share"
	"elbot/internal/events"
	"elbot/internal/request"
)

func TestFormatCommandResultRendersData(t *testing.T) {
	tests := []struct {
		name string
		kind command.ResultKind
		data any
		want string
	}{
		{name: "text", kind: command.ResultKindText, data: command.TextData{Content: "accepted"}, want: "accepted"},
		{name: "session change", data: command.SessionChangeData{Operation: "fork", Session: &command.SessionData{ID: "child", Mode: "chat", ParentSessionID: "parent", ForkFromMessageID: "message"}}, want: "fork → child · chat\nparent=parent · from=message"},
		{name: "status", data: command.StatusData{Mode: "chat", Model: command.ModelData{Provider: "p", Model: "m"}}, want: "session=nil\ndefault=chat · model=p/m"},
		{name: "doctor", data: command.DoctorData{}, want: "Everything is OK"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatCommandResult(&command.Result{Kind: tt.kind, Data: tt.data})
			if !strings.Contains(got, tt.want) {
				t.Fatalf("FormatCommandResult() = %q, want to contain %q", got, tt.want)
			}
		})
	}
}

func TestMessagePreviewsIncludeAttachments(t *testing.T) {
	for _, body := range []string{"", "body"} {
		message := command.MessageData{Role: "user", Content: body, Attachments: []command.AttachmentData{{Name: "note.txt"}, {Name: "image.png"}}}
		for _, data := range []any{
			command.MessagesData{Items: []command.MessageData{message}},
			command.SessionsData{Items: []command.SessionSummaryData{{Messages: []command.MessageData{message}}}},
			command.SessionChangeData{Operation: "resume", Session: &command.SessionData{ID: "s"}, Messages: []command.MessageData{message}},
			command.SessionChangeData{Operation: "fork", Session: &command.SessionData{ID: "s"}, Messages: []command.MessageData{message}},
		} {
			text := FormatCommandResult(&command.Result{Data: data})
			want := "user: "
			if body != "" {
				want += body + " "
			}
			want += "[附件: note.txt] [附件: image.png]"
			if !strings.Contains(text, want) {
				t.Fatalf("%T preview=%q, want %q", data, text, want)
			}
		}
	}
}

func TestSessionsPreviewKeepsNewestMessages(t *testing.T) {
	d := command.SessionsData{Items: []command.SessionSummaryData{{SessionData: command.SessionData{ID: "s-id", Title: "title"}, Messages: []command.MessageData{
		{Role: "user", Content: "omitted"}, {Role: "assistant", Content: "second"},
		{Role: "user", Content: "third"}, {Role: "assistant", Content: "fourth"}, {Role: "user", Content: "latest"},
	}}}}
	text := FormatCommandResult(&command.Result{Data: d})
	if !strings.HasPrefix(text, "[s-id] title\n") || strings.Count(text, "s-id") != 1 || strings.Contains(text, "omitted") || !strings.Contains(text, "... / bot: second / user: third / bot: fourth / user: latest") {
		t.Fatalf("sessions preview=%q", text)
	}
}

func TestDamagedLogsReportHiddenSamples(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lines []string
		want  string
	}{
		{"all", []string{"one", "two", "three"}, ""},
		{"partial", []string{"one"}, "damaged hidden=2"},
		{"none", nil, "damaged hidden=3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := FormatCommandResult(&command.Result{Data: command.LogsData{DamagedCount: 3, DamagedLines: tc.lines}})
			if !strings.Contains(text, "damaged=3") || (tc.want == "" && strings.Contains(text, "damaged hidden=")) || (tc.want != "" && !strings.Contains(text, tc.want)) {
				t.Fatal(text)
			}
			for _, line := range tc.lines {
				if !strings.Contains(text, line) {
					t.Fatalf("missing sample %q: %s", line, text)
				}
			}
		})
	}
}

func TestEmptyUsageRetainsNotices(t *testing.T) {
	for _, tc := range []struct {
		data command.UsageData
		want string
	}{
		{command.UsageData{}, "-"},
		{command.UsageData{RangeTruncated: true, AvailableCalls: 3, DamagedCount: 2}, "-\n仅有 3 次调用\nwarn: damaged=2 · usage incomplete"},
	} {
		if got := FormatCommandResult(&command.Result{Data: tc.data}); got != tc.want {
			t.Fatalf("usage=%q, want %q", got, tc.want)
		}
	}
	text := FormatCommandResult(&command.Result{Data: command.UsageData{Total: command.UsageBucket{Calls: 1}}})
	if !strings.Contains(text, "calls: 1") || !strings.Contains(text, "prompt: unk") {
		t.Fatal(text)
	}
}

func TestFormatLogsPreservesJSONValuesAndText(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
	}{
		{"object", `{"tool":"shell","elapsed_ms":1200,"summary":"done","empty":"","none":null,"large":9007199254740993}`, []string{"1.2s · done", `empty: ""`, "none: null", "large: 9007199254740993", "tool: shell"}},
		{"array", `[1,"two",null]`, []string{`[1, two, null]`}},
		{"string", `"first\nsecond"`, []string{"[first\nsecond]"}},
		{"number", `9007199254740993`, []string{"9007199254740993"}},
		{"boolean", `false`, []string{"false"}},
		{"null", `null`, []string{"null"}},
		{"newline semantics", `{"summary":"real\nnewline","literal":"line1\\nline2"}`, []string{"real\nnewline", `literal: line1\nline2`}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := FormatCommandResult(&command.Result{Kind: command.ResultKindLogs, Data: command.LogsData{Items: []command.LogData{{Type: events.ContentJSON, Content: []byte(tc.body)}}}})
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Fatalf("missing %q in %q", want, got)
				}
			}
		})
	}
}

func TestFormatLogsUsesAuditGroupsAndOmitsDuplicateCompletionEvent(t *testing.T) {
	got := FormatCommandResult(&command.Result{Kind: command.ResultKindLogs, Data: command.LogsData{Title: "audit events", Items: []command.LogData{{
		Category: events.LogAudit, EventName: "tool_call_completed", Type: events.ContentJSON,
		Content:      []byte(`{"tool":"shell","summary":"ok","arguments":{"cmd":"pwd"},"risk":"low","run_source":"foreground","elapsed_ms":20}`),
		ResultStatus: events.ResultSucceeded, Identity: command.LogIdentityData{SessionID: "s"},
	}}}})
	for _, want := range []string{"[shell] ✓ 20ms · ok", "{cmd: pwd}", "run_source: foreground", "risk: low", "session: s"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "tool_call_completed") {
		t.Fatalf("duplicate completion event shown: %q", got)
	}
}

func TestFormatLogsKeepsAuditTitlesAndProcessStates(t *testing.T) {
	for _, tc := range []struct {
		item command.LogData
		want string
	}{
		{
			item: command.LogData{Category: events.LogAudit, EventName: "llm_usage", Type: events.ContentJSON, Content: []byte(`{"provider":"openai","model":"gpt-5","elapsed_ms":999,"prompt_tokens":10,"completion_tokens":5,"cache_hit_percent":20}`)},
			want: "[openai/gpt-5] 999ms\n输入 10 · 输出 5 · 缓存 20.0%",
		},
		{
			item: command.LogData{Category: events.LogAudit, EventName: "tool_call_prepared", Type: events.ContentJSON, Content: []byte(`{"tool":"shell"}`), ResultStatus: events.ResultSucceeded},
			want: "[shell] →",
		},
		{
			item: command.LogData{Category: events.LogAudit, EventName: "risk_confirmation_wait", Type: events.ContentJSON, Content: []byte(`{}`)},
			want: "[risk_confirmation_wait] 待确认",
		},
	} {
		got := FormatCommandResult(&command.Result{Data: command.LogsData{Items: []command.LogData{tc.item}}})
		if !strings.Contains(got, tc.want) {
			t.Fatalf("missing %q in %q", tc.want, got)
		}
	}
}

func TestFormatActiveRequestsUsesReturnedOrderAndOptionalTitle(t *testing.T) {
	started := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	items := []command.RequestData{
		{ID: "root-b", Kind: request.KindTurn, Label: "b", StartedAt: started, SessionTitle: "title b"},
		{ID: "child-z", ParentID: "root-b", Kind: request.KindTool, Label: "z", StartedAt: started},
		{ID: "root-a", Kind: request.KindTurn, Label: "a", StartedAt: started, SessionTitle: "title a"},
		{ID: "child-a", ParentID: "root-b", Kind: request.KindTool, Label: "a", StartedAt: started},
	}
	withTitles := formatActiveRequestsData(items, true)
	if strings.Index(withTitles, "[1] turn: b") > strings.Index(withTitles, "[2] turn: a") || strings.Index(withTitles, "[1.1] tool: z") > strings.Index(withTitles, "[1.2] tool: a") {
		t.Fatalf("request order changed: %s", withTitles)
	}
	if !strings.Contains(withTitles, "session: title b") || strings.Contains(formatActiveRequestsData(items, false), "session: title") {
		t.Fatalf("session title control failed: %s", withTitles)
	}
}

func TestUsageNumbersArePlainAndUnknownValuesRemainUnk(t *testing.T) {
	large := int64(9007199254740993)
	data := command.UsageData{
		GroupBy: "model", Range: "1d",
		Items: []command.UsageBucket{{Key: "m", Calls: 1, PromptTokens: &large}},
		Total: command.UsageBucket{Key: "total", Calls: 1, PromptTokens: &large},
	}
	got := FormatCommandResult(&command.Result{Data: data})
	if !strings.Contains(got, "9007199254740993") || strings.Contains(got, "9,007,199,254,740,993") || !strings.Contains(got, "completion: unk") {
		t.Fatalf("usage format = %q", got)
	}
}
