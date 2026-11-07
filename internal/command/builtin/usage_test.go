package builtin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	command "elbot/internal/command/share"
	"elbot/internal/events"
	"elbot/internal/logging"
)

func makeUsageEntry(at time.Time, provider, model, sessionID string, prompt, completion, total, cache int, elapsedMS int64) logging.LogEntry {
	content, _ := json.Marshal(map[string]any{
		"provider": provider, "model": model,
		"prompt_tokens": prompt, "completion_tokens": completion, "total_tokens": total,
		"cache_hit_tokens": cache, "elapsed_ms": elapsedMS,
	})
	return logging.LogEntry{Time: at, Identity: events.LogIdentity{SessionID: sessionID}, Level: "INFO", EventName: "llm_usage", Type: events.ContentJSON, Content: string(content)}
}

func TestSummarizeUsageGroupsKnownValuesAndPreservesUnknown(t *testing.T) {
	entries := []logging.LogEntry{
		{Time: time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC), Identity: events.LogIdentity{SessionID: "s1"}, Type: events.ContentJSON, Content: `{"provider":" p ","model":" m ","prompt_tokens":9007199254740993,"completion_tokens":2,"total_tokens":null,"cache_hit_tokens":0,"elapsed_ms":10}`},
		{Time: time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC), Identity: events.LogIdentity{SessionID: "s1"}, Type: events.ContentJSON, Content: `{"provider":"p","model":"m","prompt_tokens":1,"completion_tokens":3,"total_tokens":4,"cache_hit_tokens":0,"elapsed_ms":20}`},
		{Type: events.ContentJSON, Content: `{}`},
	}
	for group, key := range map[usageGroupKey]string{groupByModel: "p/m", groupByDay: "2026-10-30", groupBySession: "s1"} {
		got := summarizeUsage(entries, group)
		if got.GroupBy != string(group) || len(got.Items) != 2 {
			t.Fatalf("group=%s data=%+v", group, got)
		}
		var selected command.UsageBucket
		for _, item := range got.Items {
			if item.Key == key {
				selected = item
			}
		}
		if selected.Calls != 2 || selected.PromptTokens == nil || *selected.PromptTokens != 9007199254740994 || selected.CompletionTokens == nil || *selected.CompletionTokens != 5 || selected.TotalTokens == nil || *selected.TotalTokens != 4 {
			t.Fatalf("group=%s item=%+v", group, selected)
		}
		if got.Total.Calls != 3 || got.Total.PromptTokens == nil || *got.Total.PromptTokens != 9007199254740994 || got.Total.TotalTokens == nil || *got.Total.TotalTokens != 4 {
			t.Fatalf("group=%s total=%+v", group, got.Total)
		}
	}
}

func TestSummarizeUsageZeroOverflowAndEmpty(t *testing.T) {
	zero := summarizeUsage([]logging.LogEntry{{Type: events.ContentJSON, Content: `{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0,"cache_hit_tokens":0,"elapsed_ms":0}`}}, groupByModel)
	if zero.Total.PromptTokens == nil || *zero.Total.PromptTokens != 0 || zero.Total.ElapsedMS == nil || *zero.Total.ElapsedMS != 0 {
		t.Fatalf("zero values lost: %+v", zero)
	}
	overflow := summarizeUsage([]logging.LogEntry{{Type: events.ContentJSON, Content: `{"prompt_tokens":9223372036854775807}`}, {Type: events.ContentJSON, Content: `{"prompt_tokens":1}`}}, groupByModel)
	if overflow.Total.PromptTokens == nil || *overflow.Total.PromptTokens != -1 {
		t.Fatalf("overflow = %+v", overflow.Total.PromptTokens)
	}
	empty := summarizeUsage(nil, groupByModel)
	if len(empty.Items) != 0 || empty.Total.Calls != 0 {
		t.Fatalf("empty = %+v", empty)
	}
}

func TestUsageCommandReturnsStructuredData(t *testing.T) {
	entries := []logging.LogEntry{
		makeUsageEntry(time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC), "p", "a", "s1", 1000, 200, 1200, 500, 1500),
		makeUsageEntry(time.Date(2026, 7, 1, 11, 0, 0, 0, time.UTC), "p", "a", "s1", 800, 100, 900, 0, 800),
		makeUsageEntry(time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC), "p", "b", "s2", 500, 50, 550, 0, 300),
	}
	service := &fakeLogService{entries: entries, damagedCount: 2}
	result, err := NewUsage(Deps{CommandText: testCommandText, Logs: service}).Handle(context.Background(), command.Request{Args: "-n 2-4 --by model"})
	if err != nil {
		t.Fatal(err)
	}
	data, ok := result.Data.(command.UsageData)
	if !ok || data.GroupBy != "model" || data.Range != "1d" || data.AvailableCalls != 3 || !data.RangeTruncated || data.DamagedCount != 2 || data.Total.Calls != 2 {
		t.Fatalf("usage result = %#v", result)
	}
	if data.Total.PromptTokens == nil || *data.Total.PromptTokens != 1300 {
		t.Fatalf("selected prompt total = %+v", data.Total.PromptTokens)
	}
}

func TestUsageQueryOptionsAndRanges(t *testing.T) {
	query, group, callRange, err := parseUsageQuery("-n 2-4 -d 7 -m gpt -s s1 --by day --since 2h")
	if err != nil || callRange == nil || callRange.start != 2 || callRange.end != 4 || query.Limit != 4 || query.Days != 7 || query.ContentFields["model"] != "gpt" || query.Fields["session_id"] != "s1" || group != groupByDay || query.Since == nil {
		t.Fatalf("query=%+v group=%s range=%+v err=%v", query, group, callRange, err)
	}
	for _, args := range []string{"-n 0", "-n 3-1", "-n 100001", "--by other"} {
		if _, _, _, err := parseUsageQuery(args); err == nil {
			t.Fatalf("parseUsageQuery(%q) succeeded", args)
		}
	}
	if !strings.Contains(usageInfo().Help, "--by") {
		t.Fatal("usage help missing group option")
	}
}
