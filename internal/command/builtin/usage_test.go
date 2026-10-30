package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"elbot/internal/command"
	"elbot/internal/events"
	"elbot/internal/logging"
)

func makeUsageEntry(t time.Time, provider, model, sessionID string, prompt, completion, total, cache int, elapsedMs int64) logging.LogEntry {
	return logging.LogEntry{
		Time:     t,
		Identity: events.LogIdentity{SessionID: &sessionID},
		Level:    "INFO",
		Content:  "audit event",
		LegacyFields: map[string]string{
			"event":             "llm_usage",
			"session_id":        sessionID,
			"provider":          provider,
			"model":             model,
			"prompt_tokens":     strconv.Itoa(prompt),
			"completion_tokens": strconv.Itoa(completion),
			"total_tokens":      strconv.Itoa(total),
			"cache_hit_tokens":  strconv.Itoa(cache),
			"elapsed_ms":        strconv.FormatInt(elapsedMs, 10),
		},
	}
}

func TestUsageJSONGroupingPreservesTokenTypes(t *testing.T) {
	sessionID := "session"
	entry := logging.LogEntry{
		Time:     time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC),
		Identity: events.LogIdentity{SessionID: &sessionID},
		Type:     events.ContentJSON,
		Content:  `{"provider":" p ","model":" m ","prompt_tokens":9007199254740993,"completion_tokens":"2","total_tokens":-1}`,
	}
	for group, key := range map[usageGroupKey]string{groupByModel: "p/m", groupByDay: "2026-10-30", groupBySession: "session"} {
		got := formatUsageSummary([]logging.LogEntry{entry}, logging.LogQuery{}, group)
		if !strings.Contains(got, "["+key+"] calls: 1") || !strings.Contains(got, "prompt: 9,007,199,254,740,993 | completion: unk | total: unk") {
			t.Fatalf("group=%s: %s", group, got)
		}
	}
	for _, body := range []string{`{"provider":12,"model":null}`, `{}`} {
		entry.Content = body
		got := formatUsageSummary([]logging.LogEntry{entry}, logging.LogQuery{}, groupByModel)
		if !strings.Contains(got, "[unk-model] calls: 1") {
			t.Fatal(got)
		}
	}
}

func TestUsageDefaultQueryByModel(t *testing.T) {
	entries := []logging.LogEntry{
		makeUsageEntry(time.Date(2026, 7, 1, 10, 0, 0, 0, time.Local), "deepseek", "deepseek-v4", "sess-1", 1000, 200, 1200, 500, 1500),
		makeUsageEntry(time.Date(2026, 7, 1, 11, 0, 0, 0, time.Local), "deepseek", "deepseek-v4", "sess-1", 800, 100, 900, 0, 800),
		makeUsageEntry(time.Date(2026, 7, 1, 12, 0, 0, 0, time.Local), "openai", "gpt-4o", "sess-2", 500, 50, 550, 0, 300),
	}
	service := &fakeLogService{entries: entries}
	result, err := NewUsage(Deps{CommandText: testCommandText, Logs: service}).Handle(context.Background(), command.Request{Args: ""})
	if err != nil {
		t.Fatalf("usage handle: %v", err)
	}
	if service.query.Prefix != "audit" || service.query.Fields["event"] != "llm_usage" {
		t.Fatalf("query = %#v", service.query)
	}
	for _, want := range []string{
		"by=model",
		"[deepseek/deepseek-v4] calls: 2",
		"prompt: 1,800 | completion: 300 | total: 2,100",
		"cache: 500 | elapsed: 2.3s",
		"[openai/gpt-4o] calls: 1",
		"prompt: 500 | completion: 50 | total: 550",
		"cache: 0 | elapsed: 0.3s",
		"[total] calls: 3",
		"prompt: 2,300 | completion: 350 | total: 2,650",
		"cache: 500 | elapsed: 2.6s",
	} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("content missing %q:\n%s", want, result.Content)
		}
	}
}

func TestUsageQueryByDay(t *testing.T) {
	entries := []logging.LogEntry{
		makeUsageEntry(time.Date(2026, 6, 30, 10, 0, 0, 0, time.Local), "deepseek", "deepseek-v4", "sess-1", 1000, 200, 1200, 0, 1000),
		makeUsageEntry(time.Date(2026, 7, 1, 10, 0, 0, 0, time.Local), "deepseek", "deepseek-v4", "sess-2", 500, 100, 600, 0, 500),
		makeUsageEntry(time.Date(2026, 7, 1, 12, 0, 0, 0, time.Local), "openai", "gpt-4o", "sess-3", 300, 50, 350, 0, 300),
	}
	service := &fakeLogService{entries: entries}
	result, err := NewUsage(Deps{CommandText: testCommandText, Logs: service}).Handle(context.Background(), command.Request{Args: "--by day -d 7"})
	if err != nil {
		t.Fatalf("usage handle: %v", err)
	}
	if service.query.Days != 7 {
		t.Fatalf("Days = %d", service.query.Days)
	}
	for _, want := range []string{
		"by=day",
		"[2026-06-30] calls: 1",
		"prompt: 1,000 | completion: 200 | total: 1,200",
		"cache: 0 | elapsed: 1.0s",
		"[2026-07-01] calls: 2",
		"prompt: 800 | completion: 150 | total: 950",
		"cache: 0 | elapsed: 0.8s",
		"[total] calls: 3",
		"prompt: 1,800 | completion: 350 | total: 2,150",
		"cache: 0 | elapsed: 1.8s",
	} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("content missing %q:\n%s", want, result.Content)
		}
	}
}

func TestUsageQueryBySession(t *testing.T) {
	entries := []logging.LogEntry{
		makeUsageEntry(time.Date(2026, 7, 1, 10, 0, 0, 0, time.Local), "deepseek", "deepseek-v4", "sess-1", 1000, 200, 1200, 100, 1000),
		makeUsageEntry(time.Date(2026, 7, 1, 11, 0, 0, 0, time.Local), "deepseek", "deepseek-v4", "sess-1", 500, 100, 600, 0, 500),
		makeUsageEntry(time.Date(2026, 7, 1, 12, 0, 0, 0, time.Local), "openai", "gpt-4o", "sess-2", 300, 50, 350, 0, 300),
	}
	service := &fakeLogService{entries: entries}
	result, err := NewUsage(Deps{CommandText: testCommandText, Logs: service}).Handle(context.Background(), command.Request{Args: "--by session"})
	if err != nil {
		t.Fatalf("usage handle: %v", err)
	}
	for _, want := range []string{
		"by=session",
		"[sess-1] calls: 2",
		"prompt: 1,500 | completion: 300 | total: 1,800",
		"cache: 100 | elapsed: 1.5s",
		"[sess-2] calls: 1",
		"prompt: 300 | completion: 50 | total: 350",
		"cache: 0 | elapsed: 0.3s",
		"[total] calls: 3",
		"prompt: 1,800 | completion: 350 | total: 2,150",
		"cache: 100 | elapsed: 1.8s",
	} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("content missing %q:\n%s", want, result.Content)
		}
	}
}

func TestUsageFiltersByModel(t *testing.T) {
	service := &fakeLogService{entries: nil}
	_, err := NewUsage(Deps{CommandText: testCommandText, Logs: service}).Handle(context.Background(), command.Request{Args: "-m gpt-4o"})
	if err != nil {
		t.Fatalf("usage handle: %v", err)
	}
	if service.query.ContentFields["model"] != "gpt-4o" {
		t.Fatalf("model filter = %q", service.query.ContentFields["model"])
	}
}

func TestUsageFiltersBySession(t *testing.T) {
	service := &fakeLogService{entries: nil}
	_, err := NewUsage(Deps{CommandText: testCommandText, Logs: service}).Handle(context.Background(), command.Request{Args: "-s sess-1"})
	if err != nil {
		t.Fatalf("usage handle: %v", err)
	}
	if service.query.Fields["session_id"] != "sess-1" {
		t.Fatalf("session filter = %q", service.query.Fields["session_id"])
	}
}

func TestUsageNoData(t *testing.T) {
	service := &fakeLogService{entries: nil}
	result, err := NewUsage(Deps{CommandText: testCommandText, Logs: service}).Handle(context.Background(), command.Request{Args: ""})
	if err != nil {
		t.Fatalf("usage handle: %v", err)
	}
	if result.Content != "nil" {
		t.Fatalf("content = %q", result.Content)
	}
}

func TestUsageSinceFilter(t *testing.T) {
	entries := []logging.LogEntry{
		makeUsageEntry(time.Date(2026, 7, 1, 10, 0, 0, 0, time.Local), "deepseek", "deepseek-v4", "sess-1", 1000, 200, 1200, 0, 1000),
	}
	service := &fakeLogService{entries: entries}
	_, err := NewUsage(Deps{CommandText: testCommandText, Logs: service}).Handle(context.Background(), command.Request{Args: "--since 2h"})
	if err != nil {
		t.Fatalf("usage handle: %v", err)
	}
	if service.query.Since == nil {
		t.Fatalf("Since should be set")
	}
}

func TestUsageInvalidByOption(t *testing.T) {
	service := &fakeLogService{entries: nil}
	_, err := NewUsage(Deps{CommandText: testCommandText, Logs: service}).Handle(context.Background(), command.Request{Args: "--by foo"})
	if err == nil || !strings.Contains(err.Error(), "must be model, day or session") {
		t.Fatalf("expected error, got: %v", err)
	}
}

func TestUsageShortFlags(t *testing.T) {
	service := &fakeLogService{entries: nil}
	_, err := NewUsage(Deps{CommandText: testCommandText, Logs: service}).Handle(context.Background(), command.Request{Args: "-d 7 -m gpt-4o -s sess-1"})
	if err != nil {
		t.Fatalf("usage handle: %v", err)
	}
	if service.query.Days != 7 || service.query.ContentFields["model"] != "gpt-4o" || service.query.Fields["session_id"] != "sess-1" {
		t.Fatalf("query = %#v", service.query)
	}
}

func TestUsageCompletionByOption(t *testing.T) {
	c := usageCommand{}
	completions := c.Complete(context.Background(), command.CompletionRequest{
		Raw: "/usage --by mo", Prefix: "/", Name: "usage", Args: "--by mo", Cursor: 15,
	})
	if len(completions) == 0 || completions[0].Text != "model" {
		t.Fatalf("expected model completion, got: %#v", completions)
	}
}

func TestUsageCompletionOptions(t *testing.T) {
	c := usageCommand{}
	completions := c.Complete(context.Background(), command.CompletionRequest{
		Raw: "/usage -", Prefix: "/", Name: "usage", Args: "-", Cursor: 8,
	})
	texts := make([]string, len(completions))
	for i, comp := range completions {
		texts[i] = comp.Text
	}
	for _, want := range []string{"-d", "--days", "-n", "-m", "--model", "-s", "--session", "--by", "--since", "--until"} {
		found := false
		for _, text := range texts {
			if text == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing completion option %q in %v", want, texts)
		}
	}
}

func TestUsageCallRangeQuery(t *testing.T) {
	for _, tc := range []struct {
		args       string
		start, end int
	}{
		{"-n 1", 1, 1},
		{"-n 2", 2, 2},
		{"-n 1-3", 1, 3},
		{"-n 2-4", 2, 4},
		{"-n 2-2", 2, 2},
		{"-n 1-100000", 1, 100000},
		{"-n 100000", 100000, 100000},
	} {
		t.Run(tc.args, func(t *testing.T) {
			query, groupBy, callRange, err := parseUsageQuery(tc.args)
			if err != nil {
				t.Fatal(err)
			}
			if callRange == nil || callRange.start != tc.start || callRange.end != tc.end {
				t.Fatalf("range = %+v", callRange)
			}
			if query.Limit != tc.end || query.Days != 1 || groupBy != groupByModel || query.Fields["session_id"] != "" {
				t.Fatalf("query = %+v, group = %s", query, groupBy)
			}
		})
	}
	query, _, callRange, err := parseUsageQuery("")
	if err != nil || callRange != nil || query.Limit != usageQueryLimit || query.Days != 1 {
		t.Fatalf("default query = %+v, range = %+v, err = %v", query, callRange, err)
	}
}

func TestUsageRejectsInvalidCallRanges(t *testing.T) {
	for _, args := range []string{
		"-n", `-n ""`, "-n 0", "-n -1", "-n 1-0", "-n 3-1", "-n 1-", "-n -2",
		"-n 1-2-3", "-n 1--2", "-n abc", "-n 1.5", "-n +1", "-n 1-+2", "-n １",
		"-n 100001", "-n 1-100001", "-n 999999999999999999999999999999", `-n "1 - 3"`,
	} {
		t.Run(args, func(t *testing.T) {
			service := &fakeLogService{}
			_, err := NewUsage(Deps{CommandText: testCommandText, Logs: service}).Handle(t.Context(), command.Request{Args: args})
			if err == nil || !strings.Contains(err.Error(), "-n") {
				t.Fatalf("expected range error, got %v", err)
			}
			if service.query.Prefix != "" {
				t.Fatal("invalid range reached log reader")
			}
		})
	}
}

func TestUsageCallRangesWithLogReader(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	yesterday := today.AddDate(0, 0, -1)
	record := func(at time.Time, event, model, session string, prompt int) string {
		t.Helper()
		data, err := json.Marshal(map[string]any{
			"time": at.Format(time.RFC3339Nano), "category": "audit", "level": "INFO",
			"event": event, "module": "chat", "type": "json", "session_id": session,
			"content": map[string]any{
				"provider": "p", "model": model, "prompt_tokens": prompt,
				"completion_tokens": prompt / 10, "total_tokens": prompt + prompt/10,
				"cache_hit_tokens": 0, "elapsed_ms": 100,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return string(data) + "\n"
	}
	for day, data := range map[time.Time]string{
		yesterday: record(yesterday.Add(8*time.Hour), "llm_usage", "a", "s1", 10),
		today: record(today.Add(8*time.Hour), "llm_usage", "a", "s1", 20) +
			record(today.Add(9*time.Hour), "llm_usage", "b", "s2", 30) +
			record(today.Add(10*time.Hour), "llm_usage", "a", "s1", 40) +
			record(today.Add(11*time.Hour), "llm_usage", "a", "s2", 50) +
			record(today.Add(12*time.Hour), "assistant_message", "a", "s2", 900),
	} {
		if err := os.WriteFile(filepath.Join(dir, "audit-"+day.Format("2006-01-02")+".log"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name, args string
		calls      int
		prompt     int
		want       []string
		notice     string
	}{
		{name: "latest", args: "-n 1", calls: 1, prompt: 50},
		{name: "second", args: "-n 2", calls: 1, prompt: 40},
		{name: "three", args: "-n 1-3", calls: 3, prompt: 120, want: []string{"[p/a] calls: 2", "[p/b] calls: 1"}},
		{name: "offset", args: "-n 2-3", calls: 2, prompt: 70},
		{name: "equal bounds", args: "-n 2-2", calls: 1, prompt: 40},
		{name: "model before range", args: "-n 1 -m b", calls: 1, prompt: 30},
		{name: "session before range", args: "-s s1 -n 2", calls: 1, prompt: 20},
		{name: "combined filters", args: "-n 2 -m a -s s1", calls: 1, prompt: 20},
		{name: "time before range", args: "-n 2 --since " + today.Add(9*time.Hour).Format("2006-01-02T15:04") + " --until " + today.Add(10*time.Hour).Format("2006-01-02T15:04"), calls: 1, prompt: 30},
		{name: "session grouping", args: "-n 1-3 --by session", calls: 3, prompt: 120, want: []string{"[s1] calls: 1", "[s2] calls: 2"}},
		{name: "cross day", args: "-n 4-5 -d 2 --by day", calls: 2, prompt: 30, want: []string{"[" + today.Format("2006-01-02") + "] calls: 1", "[" + yesterday.Format("2006-01-02") + "] calls: 1"}},
		{name: "short range today only", args: "-n 1-6", calls: 4, prompt: 140, notice: "仅有 4 次调用"},
		{name: "short range with offset", args: "-n 3-6", calls: 2, prompt: 50, notice: "仅有 4 次调用"},
		{name: "start out of range", args: "-n 5-6", notice: "仅有 4 次调用"},
		{name: "last allowed position", args: "-n 100000", notice: "仅有 4 次调用"},
		{name: "no matches", args: "-n 1 -m missing", notice: "nil"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := NewUsage(Deps{CommandText: testCommandText, Logs: logging.Reader{Dir: dir}}).Handle(t.Context(), command.Request{Args: tc.args})
			if err != nil {
				t.Fatal(err)
			}
			if tc.calls == 0 {
				if result.Content != tc.notice {
					t.Fatalf("content = %q, want %q", result.Content, tc.notice)
				}
				return
			}
			total := fmt.Sprintf("[total] calls: %d\n  prompt: %d | completion: %d | total: %d\n  cache: 0 | elapsed: %.1fs\n",
				tc.calls, tc.prompt, tc.prompt/10, tc.prompt+tc.prompt/10, float64(tc.calls)/10)
			if !strings.Contains(result.Content, total) {
				t.Fatalf("missing total %q in:\n%s", total, result.Content)
			}
			for _, want := range tc.want {
				if !strings.Contains(result.Content, want) {
					t.Fatalf("missing %q in:\n%s", want, result.Content)
				}
			}
			if tc.notice != "" {
				if !strings.HasSuffix(result.Content, "\n"+tc.notice) {
					t.Fatalf("missing exact notice %q in:\n%s", tc.notice, result.Content)
				}
			} else if strings.Contains(result.Content, "仅有") {
				t.Fatalf("unexpected shortage notice:\n%s", result.Content)
			}
		})
	}
}

func TestUsageCallRangePreservesUnknownAndDamage(t *testing.T) {
	entry := logging.LogEntry{Type: events.ContentJSON, Content: `{"provider":"p","model":"a","prompt_tokens":null,"completion_tokens":2,"total_tokens":null,"cache_hit_tokens":0,"elapsed_ms":100}`}
	for _, tc := range []struct {
		args string
		want []string
	}{
		{"-n 1", []string{"[total] calls: 1", "prompt: unk | completion: 2 | total: unk", "cache: 0 | elapsed: 0.1s"}},
		{"-n 1-3", []string{"[total] calls: 1", "仅有 1 次调用"}},
		{"-n 2", []string{"仅有 1 次调用"}},
	} {
		t.Run(tc.args, func(t *testing.T) {
			service := &fakeLogService{entries: []logging.LogEntry{entry}, damagedCount: 2}
			result, err := NewUsage(Deps{CommandText: testCommandText, Logs: service}).Handle(t.Context(), command.Request{Args: tc.args})
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range append(tc.want, "warn: damaged=2 · usage incomplete") {
				if !strings.Contains(result.Content, want) {
					t.Fatalf("missing %q in:\n%s", want, result.Content)
				}
			}
		})
	}
	result, err := NewUsage(Deps{CommandText: testCommandText, Logs: &fakeLogService{damagedCount: 2}}).Handle(t.Context(), command.Request{Args: "-n 1"})
	if err != nil || result.Content != "nil\nwarn: damaged=2 · usage incomplete" {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
}

func TestUsageCallRangeHelp(t *testing.T) {
	result, err := NewUsage(Deps{CommandText: testCommandText}).Handle(t.Context(), command.Request{Args: "--help"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"-n <x[-y]>", "筛选后按新到旧", "1..100000"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("help missing %q:\n%s", want, result.Content)
		}
	}
}
