package builtin

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"encoding/json"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"elbot/internal/command"
	"elbot/internal/events"
	"elbot/internal/logging"
	"elbot/internal/processenv"

	toolbuiltin "elbot/internal/tool/builtin"
)

func TestShellLogHasNoBlankLineBeforeRisk(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell commands")
	}
	prepared, err := toolbuiltin.NewShellTool(processenv.Environment{}, nil).PrepareCall(t.Context(), toolshare.CallRequest{Arguments: json.RawMessage(`{"cmd":"printf 'Current directory: /home/test\\n'"}`)})
	if err != nil {
		t.Fatal(err)
	}
	result, logContent, err := prepared.Call(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "Current directory: /home/test\n" {
		t.Fatalf("business output changed: %q", result.Content)
	}
	body, err := json.Marshal(struct {
		Tool    string `json:"tool"`
		Summary string `json:"summary"`
		Result  string `json:"result"`
		Risk    string `json:"risk"`
	}{"shell", logContent, result.LogContent(err), "low"})
	if err != nil {
		t.Fatal(err)
	}
	content := formatLogEntries(events.LogAudit, false)([]logging.LogEntry{{Type: events.ContentJSON, Content: string(body)}})
	want := "audit events:\nunk-time [shell] · exit_code=0\n[Current directory: /home/test]\nrisk: low"
	if content != want {
		t.Fatalf("content=%q want=%q", content, want)
	}
}

func TestUsageUnknownAndOverflowDoNotLoseCallsOrOtherTotals(t *testing.T) {
	entries := []logging.LogEntry{
		{Type: events.ContentJSON, Content: `{"provider":"p","model":"a","prompt_tokens":9223372036854775807,"completion_tokens":2,"total_tokens":null,"cache_hit_tokens":0,"elapsed_ms":10}`},
		{Type: events.ContentJSON, Content: `{"provider":"p","model":"a","prompt_tokens":1,"completion_tokens":3,"total_tokens":4,"cache_hit_tokens":0,"elapsed_ms":20}`},
		{Type: events.ContentJSON, Content: `{"provider":"p","model":"b","prompt_tokens":5,"completion_tokens":6,"total_tokens":11,"cache_hit_tokens":"illegal","elapsed_ms":30}`},
	}
	content := formatUsageSummary(entries, logging.LogQuery{Days: 1}, groupByModel)
	for _, want := range []string{"[p/a] calls: 2", "prompt: unk | completion: 5 | total: 4", "[p/b] calls: 1", "prompt: 5 | completion: 6 | total: 11", "[total] calls: 3", "prompt: unk | completion: 11 | total: 15", "cache: unk"} {
		if !strings.Contains(content, want) {
			t.Fatalf("missing %q in %s", want, content)
		}
	}
	content = formatUsageSummary([]logging.LogEntry{{Type: events.ContentJSON, Content: `{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0,"cache_hit_tokens":0,"elapsed_ms":0}`}}, logging.LogQuery{}, groupByModel)
	if !strings.Contains(content, "prompt: 0 | completion: 0 | total: 0") {
		t.Fatal(content)
	}
	content = formatUsageSummary([]logging.LogEntry{{Type: events.ContentJSON, Content: `{}`}}, logging.LogQuery{}, groupByModel)
	if !strings.Contains(content, "calls: 1") || !strings.Contains(content, "prompt: unk | completion: unk | total: unk") {
		t.Fatal(content)
	}
}

func TestDamagedLogsAreReportedWithoutMatchedEntries(t *testing.T) {
	for _, factory := range []func(Deps) command.Handler{NewLog, NewAudit, NewElwisp, NewUsage} {
		service := &fakeLogService{damagedCount: 12, damagedLines: []string{"{broken"}}
		result, err := factory(Deps{CommandText: testCommandText, Logs: service}).Handle(t.Context(), command.Request{})
		if err != nil || !strings.Contains(result.Content, "damaged=12") || !strings.Contains(result.Content, "incomplete") || strings.Contains(result.Content, "{broken") {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	}
	service := &fakeLogService{damagedCount: 12, damagedLines: []string{"{broken"}}
	result, err := NewLog(Deps{CommandText: testCommandText, Logs: service}).Handle(t.Context(), command.Request{Args: "--raw -e"})
	if err != nil || !strings.Contains(result.Content, "无法判断是否匹配筛选条件") || !strings.Contains(result.Content, "{broken") || !strings.Contains(result.Content, "damaged hidden=11") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestLogQueriesPutToolBelowHeader(t *testing.T) {
	for _, event := range []string{"tool_call_completed", "hook_tool_call_completed"} {
		for _, category := range []events.LogCategory{events.LogAudit, events.LogRuntime, events.LogElnis} {
			title := map[events.LogCategory]string{events.LogAudit: "audit events", events.LogRuntime: "runtime logs", events.LogElnis: "elwisp logs"}[category]
			entry := logging.LogEntry{
				Level:     "INFO",
				EventName: event,
				Type:      events.ContentJSON,
				Content:   `{"tool":"shell","result":"done","actor_id":"user","nested":{"tool":"nested","actor_id":"inner"}}`,
				Raw:       "ORIGINAL_PHYSICAL_RECORD",
			}
			content := formatLogEntries(category, false)([]logging.LogEntry{entry})
			if !strings.Contains(content, "[done]\n") || !strings.Contains(content, "actor: user") || !strings.Contains(content, "nested: {actor_id: inner · tool: nested}") || !strings.HasPrefix(content, title+":\n") {
				t.Fatalf("unexpected fields for %s: %s", event, content)
			}
			if category == events.LogAudit && !strings.Contains(content, "[shell]") || category != events.LogAudit && !strings.Contains(content, "tool: shell") {
				t.Fatalf("tool missing for %s: %s", event, content)
			}
			raw := formatLogEntries(category, true)([]logging.LogEntry{entry})
			_, rawBody, found := strings.Cut(raw, "\n")
			if !found || strings.TrimSpace(rawBody) != entry.Raw || !strings.HasPrefix(raw, title+":\n") {
				t.Fatalf("raw record changed: %s", raw)
			}
		}
	}
	entry := logging.LogEntry{Type: events.ContentJSON, Content: `{"result":"done","actor_id":"user"}`}
	if content := formatLogEntries(events.LogRuntime, false)([]logging.LogEntry{entry}); content != "runtime logs:\nunk-time\n[done]\nactor: user" {
		t.Fatalf("non-tool field order changed: %s", content)
	}
}

func TestLogQueryResultHeaders(t *testing.T) {
	for _, category := range []events.LogCategory{events.LogAudit, events.LogRuntime, events.LogElnis} {
		for _, tc := range []struct {
			status events.ResultStatus
			suffix string
		}{
			{events.ResultUnspecified, ""}, {events.ResultSucceeded, " ✓"},
			{events.ResultFailed, " ✗"}, {events.ResultCanceled, " 已取消"},
			{events.ResultRejected, " 已拒绝"}, {events.ResultSkipped, " 已跳过"},
		} {
			entry := logging.LogEntry{Level: "INFO", EventName: "event", ResultStatus: tc.status, Content: "body"}
			got := formatLogEntries(category, false)([]logging.LogEntry{entry})
			subject := "[event]"
			if category == events.LogRuntime {
				subject = "event"
			}
			want := formatLogEntryTime(entry) + " " + subject + tc.suffix + "\n[body]\nINFO"
			if !strings.HasSuffix(got, want) {
				t.Fatalf("got=%q want suffix=%q", got, want)
			}
		}
	}
}

func TestAuditPreservesSourceOrderThroughStorageAndFiltering(t *testing.T) {
	center, err := logging.NewManager("debug", filepath.Join(t.TempDir(), "sessions.sqlite"), 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = center.Close(context.Background()) })
	summary := strings.Repeat("摘要", 600) + "SUMMARY_TAIL\nsecond summary line"
	summaryJSON, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"tool":"shell","summary":` + string(summaryJSON) + `,"run_source":"foreground","risk":"low","result":"first\nsecond","actor_id":"user","nested":{"z":9007199254740993,"a":[]}}`
	if err := events.EmitLog(t.Context(), events.LogRecord{Category: events.LogAudit, EventName: "tool_call_completed", Module: "toolrun", Type: events.ContentJSON, Content: body, ResultStatus: events.ResultSucceeded}); err != nil {
		t.Fatal(err)
	}
	if err := center.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	reader := logging.Reader{Dir: center.LogDir()}
	query, err := parseAuditQuery("-t --tool shell --run_source foreground --risk low")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := reader.Query(t.Context(), query)
	if err != nil || len(entries.Entries) != 1 || entries.Entries[0].Content != body {
		t.Fatalf("stored content changed: entries=%+v err=%v", entries, err)
	}
	result, err := NewAudit(Deps{CommandText: testCommandText, Logs: reader}).Handle(t.Context(), command.Request{Args: "-t --tool shell --run_source foreground --risk low"})
	if err != nil {
		t.Fatal(err)
	}
	want := " [shell] ✓ · " + summary + "\n[first\nsecond]\ntool_call_completed · actor: user · run_source: foreground\nINFO · risk: low\nnested: {a: [] · z: 9007199254740993}"
	if !strings.HasSuffix(result.Content, want) {
		t.Fatalf("display order changed: %s", result.Content)
	}
	query.ContentFields["tool"] = "different"
	entries, err = reader.Query(t.Context(), query)
	if err != nil || len(entries.Entries) != 0 {
		t.Fatalf("field filter failed: entries=%+v err=%v", entries, err)
	}
}

func TestLogQueriesRenderDecodedJSONStrings(t *testing.T) {
	for _, factory := range []func(Deps) command.Handler{NewLog, NewAudit, NewElwisp} {
		service := &fakeLogService{entries: []logging.LogEntry{{
			Type:    events.ContentJSON,
			Content: `{"result":"first\n\tsecond","literal":"printf '\\n'","nested":{"items":["line one\nline two",9007199254740993]},"empty":[],"flag":false,"none":null}`,
			Raw:     "ORIGINAL_PHYSICAL_RECORD",
		}}}
		result, err := factory(Deps{CommandText: testCommandText, Logs: service}).Handle(t.Context(), command.Request{})
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"first\n", "\tsecond", "line one\n", "line two", `printf '\n'`, "9007199254740993", "false", "null", "[]"} {
			if !strings.Contains(result.Content, want) {
				t.Errorf("missing decoded value %q in %s", want, result.Content)
			}
		}
		if strings.Contains(result.Content, `first\n\tsecond`) || strings.Contains(result.Content, `line one\nline two`) {
			t.Errorf("JSON escapes leaked into normal display: %s", result.Content)
		}
		result, err = factory(Deps{CommandText: testCommandText, Logs: service}).Handle(t.Context(), command.Request{Args: "--raw"})
		if err != nil || !strings.Contains(result.Content, service.entries[0].Raw) {
			t.Fatalf("raw display changed: result=%+v err=%v", result, err)
		}
	}
}
