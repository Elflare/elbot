package format

import (
	"strings"
	"testing"
	"time"

	"elbot/internal/control"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/request"
)

func TestDoctorFormattingRetainsConfigPathsAndFields(t *testing.T) {
	got := formatTestResult(&testResult{Data: control.CheckConfigurationResult{
		ConfigPath: "/config/app.toml",
		Items:      []control.DoctorIssue{{Path: "/config/providers.toml", Level: "error", Field: []string{"platform", "telegram", "enabled"}, Message: "类型错误。"}},
	}})
	for _, want := range []string{"主配置：/config/app.toml", "/config/providers.toml", "[错误]", "platform.telegram.enabled", "类型错误。"} {
		if !strings.Contains(got, want) {
			t.Fatalf("doctor output missing %q: %s", want, got)
		}
	}
}

func TestMessagePreviewsIncludeAttachments(t *testing.T) {
	for _, body := range []string{"", "body"} {
		message := control.MessageData{Role: "user", Content: body, Segments: []llm.MessageSegment{{Type: llm.SegmentFile, Name: "note.txt"}, {Type: llm.SegmentImage, Name: "image.png"}}}
		change := control.SessionChangeResult{Session: &control.SessionData{ID: "s"}, Messages: []control.MessageData{message}}
		texts := []string{
			Messages(control.SessionMessagesResult{Items: []control.MessageData{message}}, MessagesOptions{PreviewLimit: 40}),
			Sessions(control.ListSessionsResult{Items: []control.SessionSummaryData{{Messages: []control.MessageData{message}}}}, SessionsOptions{MessageLimit: 4, PreviewLimit: 24}),
			SessionChange("resume", change, MessagesOptions{PreviewLimit: 80}),
			SessionChange("fork", change, MessagesOptions{PreviewLimit: 80}),
		}
		for _, text := range texts {
			want := "user: "
			if body != "" {
				want += body + " "
			}
			want += "[附件: note.txt] [附件: image.png]"
			if !strings.Contains(text, want) {
				t.Fatalf("preview=%q want=%q", text, want)
			}
		}
	}
}

func TestSessionsPreviewKeepsNewestMessages(t *testing.T) {
	d := control.ListSessionsResult{Items: []control.SessionSummaryData{{SessionData: control.SessionData{ID: "s-id", Title: "title"}, Messages: []control.MessageData{
		{Role: "user", Content: "omitted"}, {Role: "assistant", Content: "second"},
		{Role: "user", Content: "third"}, {Role: "assistant", Content: "fourth"}, {Role: "user", Content: "latest"},
	}}}}
	text := formatTestResult(&testResult{Data: d})
	if !strings.HasPrefix(text, "[s-id] title\n") || strings.Count(text, "s-id") != 1 || strings.Contains(text, "omitted") || !strings.Contains(text, "... / bot: second / user: third / bot: fourth / user: latest") {
		t.Fatalf("sessions preview=%q", text)
	}
}

func TestSessionsPageLinksUseCallerCommands(t *testing.T) {
	d := control.ListSessionsResult{Page: control.Page{Page: 2, PageSize: 10, HasNext: true}}
	text := Sessions(d, SessionsOptions{PrevCommand: "!sessions 1 -k query text", NextCommand: "!sessions 3 -k query text"})
	if !strings.Contains(text, "prev: !sessions 1 -k query text") || !strings.Contains(text, "next: !sessions 3 -k query text") {
		t.Fatal(text)
	}
	text = Sessions(d, SessionsOptions{PrevCommand: "!resume --page 1", NextCommand: "!resume --page 3"})
	if !strings.Contains(text, "prev: !resume --page 1") || !strings.Contains(text, "next: !resume --page 3") {
		t.Fatal(text)
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
			text := formatTestResult(&testResult{Data: control.QueryLogsResult{DamagedCount: 3, DamagedLines: tc.lines}})
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
		data control.QueryUsageResult
		want string
	}{
		{control.QueryUsageResult{}, "-"},
		{control.QueryUsageResult{RangeTruncated: true, AvailableCalls: 3, DamagedCount: 2}, "-\n仅有 3 次调用\nwarn: damaged=2 · usage incomplete"},
	} {
		if got := formatTestResult(&testResult{Data: tc.data}); got != tc.want {
			t.Fatalf("usage=%q, want %q", got, tc.want)
		}
	}
	text := formatTestResult(&testResult{Data: control.QueryUsageResult{Total: control.UsageBucket{Calls: 1}}})
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
			got := formatTestResult(&testResult{Kind: "logs", Data: control.QueryLogsResult{Items: []control.LogData{{Type: events.ContentJSON, Content: []byte(tc.body)}}}})
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Fatalf("missing %q in %q", want, got)
				}
			}
		})
	}
}

func TestFormatLogsUsesAuditGroupsAndOmitsDuplicateCompletionEvent(t *testing.T) {
	got := formatTestResult(&testResult{Kind: "logs", Data: control.QueryLogsResult{Items: []control.LogData{{
		Category: events.LogAudit, EventName: "tool_call_completed", Type: events.ContentJSON,
		Content:      []byte(`{"tool":"shell","summary":"ok","arguments":{"cmd":"pwd"},"risk":"low","run_source":"foreground","elapsed_ms":20}`),
		ResultStatus: events.ResultSucceeded, Identity: control.LogIdentityData{SessionID: "s"},
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
		item control.LogData
		want string
	}{
		{
			item: control.LogData{Category: events.LogAudit, EventName: "llm_usage", Type: events.ContentJSON, Content: []byte(`{"provider":"openai","model":"gpt-5","elapsed_ms":999,"prompt_tokens":10,"completion_tokens":5,"cache_hit_percent":20}`)},
			want: "[openai/gpt-5] 999ms\n输入 10 · 输出 5 · 缓存 20.0%",
		},
		{
			item: control.LogData{Category: events.LogAudit, EventName: "tool_call_prepared", Type: events.ContentJSON, Content: []byte(`{"tool":"shell"}`), ResultStatus: events.ResultSucceeded},
			want: "[shell] →",
		},
		{
			item: control.LogData{Category: events.LogAudit, EventName: "risk_confirmation_wait", Type: events.ContentJSON, Content: []byte(`{}`)},
			want: "[risk_confirmation_wait] 待确认",
		},
	} {
		got := formatTestResult(&testResult{Data: control.QueryLogsResult{Items: []control.LogData{tc.item}}})
		if !strings.Contains(got, tc.want) {
			t.Fatalf("missing %q in %q", tc.want, got)
		}
	}
}

func TestFormatActiveRequestsUsesReturnedOrderAndOptionalTitle(t *testing.T) {
	started := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	items := []control.RequestData{
		{ID: "root-b", Kind: request.KindTurn, Label: "b", StartedAt: started, SessionTitle: "title b"},
		{ID: "child-z", ParentID: "root-b", Kind: request.KindTool, Label: "z", StartedAt: started},
		{ID: "root-a", Kind: request.KindTurn, Label: "a", StartedAt: started, SessionTitle: "title a"},
		{ID: "child-a", ParentID: "root-b", Kind: request.KindTool, Label: "a", StartedAt: started},
	}
	withTitles := activeRequests(items, true)
	if strings.Index(withTitles, "[1] turn: b") > strings.Index(withTitles, "[2] turn: a") || strings.Index(withTitles, "[1.1] tool: z") > strings.Index(withTitles, "[1.2] tool: a") {
		t.Fatalf("request order changed: %s", withTitles)
	}
	if !strings.Contains(withTitles, "session: title b") || strings.Contains(activeRequests(items, false), "session: title") {
		t.Fatalf("session title control failed: %s", withTitles)
	}
}

func TestUsageNumbersArePlainAndUnknownValuesRemainUnk(t *testing.T) {
	large := int64(9007199254740993)
	data := control.QueryUsageResult{
		GroupBy: "model",
		Items:   []control.UsageBucket{{Key: "m", Calls: 1, PromptTokens: &large}},
		Total:   control.UsageBucket{Key: "total", Calls: 1, PromptTokens: &large},
	}
	got := formatTestResult(&testResult{Data: data})
	if !strings.Contains(got, "9007199254740993") || strings.Contains(got, "9,007,199,254,740,993") || !strings.Contains(got, "completion: unk") {
		t.Fatalf("usage format = %q", got)
	}
}

type testResult struct {
	Kind string
	Data any
}

func formatTestResult(r *testResult) string {
	switch d := r.Data.(type) {
	case control.CheckConfigurationResult:
		return Doctor(d)
	case control.ListSessionsResult:
		return Sessions(d, SessionsOptions{MessageLimit: 4, PreviewLimit: 24})
	case control.QueryLogsResult:
		return Logs("audit events", d)
	case control.QueryUsageResult:
		return Usage(d)
	default:
		panic("unhandled test data")
	}
}
