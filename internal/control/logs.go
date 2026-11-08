package control

import (
	"context"
	"encoding/json"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"

	"elbot/internal/events"
	"elbot/internal/logging"
)

func (m *Supervisor) QueryLogs(ctx context.Context, req QueryLogsRequest) (QueryLogsResult, error) {
	result := QueryLogsResult{Items: []LogData{}, DamagedLines: []string{}}
	if err := authorize(ctx, true, m.deps.Logs != nil); err != nil {
		return result, err
	}
	if !slices.Contains([]string{"log", "audit", "elnis"}, req.Category) {
		return result, invalid("category 必须是 log、audit 或 elnis")
	}
	query := req.Query
	query.Prefix = req.Category
	listed, err := m.deps.Logs.Query(ctx, query)
	if err != nil {
		return result, ClassifyError(err)
	}
	result.DamagedCount = listed.DamagedCount
	if query.Raw {
		result.DamagedLines = append(result.DamagedLines, listed.DamagedLines...)
	}
	for _, entry := range listed.Entries {
		content := json.RawMessage(entry.Content)
		if entry.Type != events.ContentJSON {
			content, _ = json.Marshal(entry.Content)
		}
		item := LogData{Time: entry.Time, Category: entry.Category, Level: entry.Level, EventName: entry.EventName, Module: entry.Module, Type: entry.Type, Content: content,
			Identity: LogIdentityData{SessionID: entry.Identity.SessionID, RunID: entry.Identity.RunID, Attempt: entry.Identity.Attempt, RequestID: entry.Identity.RequestID, RootRequestID: entry.Identity.RootRequestID}, ResultStatus: entry.ResultStatus}
		if query.Raw {
			item.Raw = entry.Raw
		}
		result.Items = append(result.Items, item)
	}
	return result, nil
}

func (m *Supervisor) QueryUsage(ctx context.Context, req QueryUsageRequest) (QueryUsageResult, error) {
	result := QueryUsageResult{Items: []UsageBucket{}}
	if err := authorize(ctx, true, m.deps.Logs != nil); err != nil {
		return result, err
	}
	if !slices.Contains([]string{"model", "day", "session"}, req.GroupBy) {
		return result, invalid("group_by 必须是 model、day 或 session")
	}
	if req.First < 1 || req.Last < req.First {
		return result, invalid("调用区间必须满足 1 <= first <= last")
	}
	query := req.Query
	query.Prefix = "audit"
	query.Limit = req.Last
	query.Fields = maps.Clone(query.Fields)
	if query.Fields == nil {
		query.Fields = map[string]string{}
	}
	query.Fields["event"] = "llm_usage"
	query.Events = nil
	listed, err := m.deps.Logs.Query(ctx, query)
	if err != nil {
		return result, ClassifyError(err)
	}
	result.GroupBy = req.GroupBy
	result.DamagedCount = listed.DamagedCount
	result.AvailableCalls = len(listed.Entries)
	result.RangeTruncated = result.AvailableCalls < req.Last
	selected := listed.Entries[min(req.First-1, len(listed.Entries)):min(req.Last, len(listed.Entries))]
	buckets := map[string]*usageAccumulator{}
	grand := &usageAccumulator{}
	for _, entry := range selected {
		var fields map[string]json.RawMessage
		_ = json.Unmarshal([]byte(entry.Content), &fields)
		key := usageKey(entry, req.GroupBy, fields)
		if buckets[key] == nil {
			buckets[key] = &usageAccumulator{}
		}
		values := [5]json.RawMessage{fields["prompt_tokens"], fields["completion_tokens"], fields["total_tokens"], fields["cache_hit_tokens"], fields["elapsed_ms"]}
		buckets[key].add(values)
		grand.add(values)
	}
	keys := make([]string, 0, len(buckets))
	for key := range buckets {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		result.Items = append(result.Items, buckets[key].result(key))
	}
	result.Total = grand.result("total")
	return result, nil
}

type usageAccumulator struct {
	calls   int
	totals  [5]int64 // 顺序为 prompt、completion、total、cache、elapsed。
	unknown [5]bool  // 任一缺失、非法值或溢出会永久令该组计数未知。
}

func (b *usageAccumulator) add(values [5]json.RawMessage) {
	b.calls++
	for i, raw := range values {
		value, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
		if err != nil || value < 0 || value > math.MaxInt64-b.totals[i] {
			b.unknown[i] = true
			continue
		}
		b.totals[i] += value
	}
}

func (b *usageAccumulator) result(key string) UsageBucket {
	var values [5]*int64
	for i, value := range b.totals {
		if b.calls > 0 && !b.unknown[i] {
			values[i] = new(value)
		}
	}
	return UsageBucket{Key: key, Calls: b.calls, PromptTokens: values[0], CompletionTokens: values[1], TotalTokens: values[2], CacheHitTokens: values[3], ElapsedMS: values[4]}
}

func usageKey(entry logging.LogEntry, group string, fields map[string]json.RawMessage) string {
	switch group {
	case "day":
		return entry.Time.Format("2006-01-02")
	case "session":
		return entry.Identity.SessionID
	default:
		var provider, model string
		_ = json.Unmarshal(fields["provider"], &provider)
		_ = json.Unmarshal(fields["model"], &model)
		return provider + "/" + model
	}
}
