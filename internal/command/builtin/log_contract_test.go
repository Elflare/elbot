package builtin

import (
	"strings"
	"testing"

	"elbot/internal/command"
	"elbot/internal/events"
	"elbot/internal/logging"
)

func TestUsageUnknownAndOverflowDoNotLoseCallsOrOtherTotals(t *testing.T) {
	entries := []logging.LogEntry{
		{Type: events.ContentJSON, Content: `{"provider":"p","model":"a","prompt_tokens":9223372036854775807,"completion_tokens":2,"total_tokens":null,"cache_hit_tokens":0,"elapsed_ms":10}`},
		{Type: events.ContentJSON, Content: `{"provider":"p","model":"a","prompt_tokens":1,"completion_tokens":3,"total_tokens":4,"cache_hit_tokens":0,"elapsed_ms":20}`},
		{Type: events.ContentJSON, Content: `{"provider":"p","model":"b","prompt_tokens":5,"completion_tokens":6,"total_tokens":11,"cache_hit_tokens":"illegal","elapsed_ms":30}`},
	}
	content := formatUsageSummary(entries, logging.LogQuery{Days: 1}, groupByModel)
	for _, want := range []string{"[p/a] calls: 2", "prompt: null | completion: 5 | total: null", "[p/b] calls: 1", "prompt: 5 | completion: 6 | total: 11", "[total] calls: 3", "prompt: null | completion: 11 | total: null", "cache: null"} {
		if !strings.Contains(content, want) {
			t.Fatalf("missing %q in %s", want, content)
		}
	}
	content = formatUsageSummary([]logging.LogEntry{{Type: events.ContentJSON, Content: `{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0,"cache_hit_tokens":0,"elapsed_ms":0}`}}, logging.LogQuery{}, groupByModel)
	if !strings.Contains(content, "prompt: 0 | completion: 0 | total: 0") {
		t.Fatal(content)
	}
	content = formatUsageSummary([]logging.LogEntry{{Type: events.ContentJSON, Content: `{}`}}, logging.LogQuery{}, groupByModel)
	if !strings.Contains(content, "calls: 1") || !strings.Contains(content, "prompt: null | completion: null | total: null") {
		t.Fatal(content)
	}
}

func TestDamagedLogsAreReportedWithoutMatchedEntries(t *testing.T) {
	for _, factory := range []func(Deps) command.Handler{NewLog, NewAudit, NewElwisp, NewUsage} {
		service := &fakeLogService{damagedCount: 12, damagedLines: []string{"{broken"}}
		result, err := factory(Deps{Logs: service}).Handle(t.Context(), command.Request{})
		if err != nil || !strings.Contains(result.Content, "12 条损坏记录") || !strings.Contains(result.Content, "可能不完整") || strings.Contains(result.Content, "{broken") {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	}
	service := &fakeLogService{damagedCount: 12, damagedLines: []string{"{broken"}}
	result, err := NewLog(Deps{Logs: service}).Handle(t.Context(), command.Request{Args: "--raw -e"})
	if err != nil || !strings.Contains(result.Content, "无法判断是否匹配筛选条件") || !strings.Contains(result.Content, "{broken") || !strings.Contains(result.Content, "11 条损坏记录未展示") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestLogQueriesRenderDecodedJSONStrings(t *testing.T) {
	for _, factory := range []func(Deps) command.Handler{NewLog, NewAudit, NewElwisp} {
		service := &fakeLogService{entries: []logging.LogEntry{{
			Type:    events.ContentJSON,
			Content: `{"result":"first\n\tsecond","literal":"printf '\\n'","nested":{"items":["line one\nline two",9007199254740993]},"empty":[],"flag":false,"none":null}`,
			Raw:     "ORIGINAL_PHYSICAL_RECORD",
		}}}
		result, err := factory(Deps{Logs: service}).Handle(t.Context(), command.Request{})
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
		result, err = factory(Deps{Logs: service}).Handle(t.Context(), command.Request{Args: "--raw"})
		if err != nil || !strings.Contains(result.Content, service.entries[0].Raw) {
			t.Fatalf("raw display changed: result=%+v err=%v", result, err)
		}
	}
}
