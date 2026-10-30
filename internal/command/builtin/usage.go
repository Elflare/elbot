package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"elbot/internal/command"
	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/logging"
)

const usageQueryLimit = 100000

func usageInfo() command.Info {
	return command.Info{
		Name:        "usage",
		Args:        "[options]",
		Description: "用量统计",
		MinRole:     contextinfo.RoleSuperadmin,
		Help: strings.TrimSpace(`-d, --days <n>       天数，默认 1
-n <x[-y]>           筛选后按新到旧取第 x..y 次，1..100000
-m, --model <name>   模型
-s, --session <id>   会话
--by model|day|session  分组，默认 model
--since/--until <time>  时间范围，如 2h、2026-06-03`),
	}
}

func NewUsage(deps Deps) command.Handler {
	return usageCommand{deps: deps}
}

type usageCommand struct {
	deps Deps
}

func (c usageCommand) Info() command.Info { return usageInfo() }

func (c usageCommand) Handle(ctx context.Context, req command.Request) (*command.Result, error) {
	if wantsCommandHelp(req.Args) {
		return formatCommandHelp(c.deps.CommandText, usageInfo()), nil
	}
	query, groupBy, callRange, err := parseUsageQuery(req.Args)
	if err != nil {
		return nil, err
	}
	if c.deps.Logs == nil {
		return &command.Result{Content: "logs: not configured"}, nil
	}
	entries, err := c.deps.Logs.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	content := "nil"
	if callRange == nil {
		if len(entries.Entries) > 0 {
			content = formatUsageSummary(entries.Entries, query, groupBy)
		}
	} else {
		content = "nil"
		if len(entries.Entries) > 0 {
			if len(entries.Entries) >= callRange.start {
				content = formatUsageSummary(entries.Entries[callRange.start-1:min(callRange.end, len(entries.Entries))], query, groupBy)
			} else {
				content = ""
			}
			if len(entries.Entries) < callRange.end {
				if content != "" {
					content += "\n"
				}
				content += fmt.Sprintf("仅有 %d 次调用", len(entries.Entries))
			}
		}
	}
	if entries.DamagedCount > 0 {
		content += fmt.Sprintf("\nwarn: damaged=%d · usage incomplete", entries.DamagedCount)
	}
	return &command.Result{Content: content}, nil
}

func (c usageCommand) Complete(ctx context.Context, req command.CompletionRequest) []command.Completion {
	_ = ctx
	token := command.CurrentCompletionToken(req)
	previous := previousLogToken(req.Raw, token.Start)
	if previous == "--by" {
		return completeStringOptions([]string{"model", "day", "session"}, token.Text, token.Start, token.End, "group_by")
	}
	if strings.HasPrefix(token.Text, "-") || token.Text == "" {
		return command.CompleteStaticOptions(usageCompletionOptions(), token.Text, token.Start, token.End, "option")
	}
	return nil
}

func usageCompletionOptions() []command.CompletionOption {
	return []command.CompletionOption{
		{Text: "-d", Description: "天数"},
		{Text: "--days", Description: "天数"},
		{Text: "-n", Description: "按新到旧取 x 或 x-y 次"},
		{Text: "-m", Description: "模型过滤"},
		{Text: "--model", Description: "模型过滤"},
		{Text: "-s", Description: "会话过滤"},
		{Text: "--session", Description: "会话过滤"},
		{Text: "--by", Description: "分组: model/day/session"},
		{Text: "--since", Description: "起始时间"},
		{Text: "--until", Description: "截止时间"},
	}
}

type usageGroupKey string

const (
	groupByModel   usageGroupKey = "model"
	groupByDay     usageGroupKey = "day"
	groupBySession usageGroupKey = "session"
)

type usageCallRange struct {
	// start and end are inclusive call positions, numbered from 1 (newest) after filtering.
	start int
	end   int
}

func parseUsageCallRange(value string) (*usageCallRange, error) {
	parts := strings.Split(value, "-")
	rangeErr := fmt.Errorf("-n must be x or x-y with 1 <= x <= y <= %d", usageQueryLimit)
	if len(parts) > 2 {
		return nil, rangeErr
	}
	callRange := &usageCallRange{}
	for i, part := range parts {
		parsed, err := strconv.ParseUint(part, 10, 32)
		if err != nil || parsed == 0 || parsed > usageQueryLimit {
			return nil, rangeErr
		}
		if i == 0 {
			callRange.start = int(parsed)
		}
		callRange.end = int(parsed)
	}
	if callRange.start > callRange.end {
		return nil, rangeErr
	}
	return callRange, nil
}

func parseUsageQuery(args string) (logging.LogQuery, usageGroupKey, *usageCallRange, error) {
	query := logging.LogQuery{
		Prefix:        "audit",
		Limit:         usageQueryLimit,
		Days:          1,
		MinLevel:      "debug",
		Fields:        map[string]string{"event": "llm_usage"},
		ContentFields: map[string]string{},
	}
	groupBy := groupByModel
	var callRange *usageCallRange

	parts, err := splitLogArgs(args)
	if err != nil {
		return query, groupBy, callRange, err
	}
	for i := 0; i < len(parts); i++ {
		name := parts[i]
		switch name {
		case "-n":
			value, err := nextArg(parts, &i, name)
			if err != nil {
				return query, groupBy, callRange, err
			}
			callRange, err = parseUsageCallRange(value)
			if err != nil {
				return query, groupBy, callRange, err
			}
			query.Limit = callRange.end
		case "-d", "--days":
			value, err := nextArg(parts, &i, name)
			if err != nil {
				return query, groupBy, callRange, err
			}
			days, err := parsePositiveInt(value, "days")
			if err != nil {
				return query, groupBy, callRange, err
			}
			query.Days = days
		case "-m", "--model":
			value, err := nextArg(parts, &i, name)
			if err != nil {
				return query, groupBy, callRange, err
			}
			query.ContentFields["model"] = value
		case "-s", "--session":
			value, err := nextArg(parts, &i, name)
			if err != nil {
				return query, groupBy, callRange, err
			}
			query.Fields["session_id"] = value
		case "--by":
			value, err := nextArg(parts, &i, name)
			if err != nil {
				return query, groupBy, callRange, err
			}
			switch strings.ToLower(strings.TrimSpace(value)) {
			case "model", "day", "session":
				groupBy = usageGroupKey(strings.ToLower(strings.TrimSpace(value)))
			default:
				return query, groupBy, callRange, fmt.Errorf("--by must be model, day or session")
			}
		case "--since":
			value, err := nextArg(parts, &i, name)
			if err != nil {
				return query, groupBy, callRange, err
			}
			parsed, err := parseLogTime(value)
			if err != nil {
				return query, groupBy, callRange, err
			}
			query.Since = &parsed
		case "--until":
			value, err := nextArg(parts, &i, name)
			if err != nil {
				return query, groupBy, callRange, err
			}
			parsed, err := parseLogTime(value)
			if err != nil {
				return query, groupBy, callRange, err
			}
			query.Until = &parsed
		default:
			return query, groupBy, callRange, fmt.Errorf("unknown option: %s", name)
		}
	}
	return query, groupBy, callRange, nil
}

type usageBucket struct {
	calls int
	// Token and elapsed totals use -1 for unknown; overflow also becomes unknown.
	promptTokens     int64
	completionTokens int64
	totalTokens      int64
	cacheHitTokens   int64
	elapsedMs        int64
}

func formatUsageSummary(entries []logging.LogEntry, query logging.LogQuery, groupBy usageGroupKey) string {
	buckets := map[string]*usageBucket{}
	var order []string
	grand := &usageBucket{}

	for _, entry := range entries {
		fields := entry.LegacyFields
		var content map[string]json.RawMessage
		if entry.Type == events.ContentJSON {
			_ = json.Unmarshal([]byte(entry.Content), &content)
			fields = map[string]string{}
			for key, value := range content {
				fields[key] = string(value)
			}
		}
		key := usageGroupKeyFor(entry, groupBy, content)
		b := buckets[key]
		if b == nil {
			b = &usageBucket{}
			buckets[key] = b
			order = append(order, key)
		}
		prompt := atoiSafe(fields["prompt_tokens"])
		completion := atoiSafe(fields["completion_tokens"])
		total := atoiSafe(fields["total_tokens"])
		cache := atoiSafe(fields["cache_hit_tokens"])
		elapsed := atoiSafe(fields["elapsed_ms"])
		for _, b := range []*usageBucket{b, grand} {
			b.calls++
			for total, value := range map[*int64]int64{&b.promptTokens: prompt, &b.completionTokens: completion, &b.totalTokens: total, &b.cacheHitTokens: cache, &b.elapsedMs: elapsed} {
				if *total < 0 || value < 0 || value > math.MaxInt64-*total {
					*total = -1
				} else {
					*total += value
				}
			}
		}
	}

	sort.Strings(order)

	rangeDesc := usageRangeDesc(query)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("usage: %s · by=%s\n", rangeDesc, groupBy))
	for _, key := range order {
		b := buckets[key]
		formatUsageBucket(&sb, key, b)
		sb.WriteString("\n")
	}
	formatUsageBucket(&sb, "total", grand)
	return sb.String()
}

func usageGroupKeyFor(entry logging.LogEntry, groupBy usageGroupKey, content map[string]json.RawMessage) string {
	switch groupBy {
	case groupByDay:
		if entry.Time.IsZero() {
			return "unk-date"
		}
		return entry.Time.Format("2006-01-02")
	case groupBySession:
		if entry.Identity.SessionID != nil {
			return firstNonEmpty(*entry.Identity.SessionID, "unk-session")
		}
		return "unk-session"
	default:
		var provider, model string
		if entry.Type == events.ContentJSON {
			_ = json.Unmarshal(content["provider"], &provider)
			_ = json.Unmarshal(content["model"], &model)
		} else {
			provider, model = entry.LegacyFields["provider"], entry.LegacyFields["model"]
		}
		provider = strings.TrimSpace(provider)
		model = strings.TrimSpace(model)
		if provider != "" && model != "" {
			return provider + "/" + model
		}
		return firstNonEmpty(model, provider, "unk-model")
	}
}

func usageRangeDesc(query logging.LogQuery) string {
	parts := []string{}
	if query.Since != nil {
		parts = append(parts, "since "+query.Since.Format("2006-01-02 15:04"))
	}
	if query.Until != nil {
		parts = append(parts, "until "+query.Until.Format("2006-01-02 15:04"))
	}
	if len(parts) > 0 {
		return strings.Join(parts, ", ")
	}
	if query.Days > 0 {
		return fmt.Sprintf("%dd", query.Days)
	}
	return "all"
}

func atoiSafe(value string) int64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return -1
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < 0 {
		return -1
	}
	return n
}

func formatThousands(n int64) string {
	if n < 0 {
		return "unk"
	}
	if n == 0 {
		return "0"
	}
	digits := strconv.FormatInt(n, 10)
	if len(digits) <= 3 {
		return digits
	}
	var sb strings.Builder
	rem := len(digits) % 3
	if rem > 0 {
		sb.WriteString(digits[:rem])
		if len(digits) > rem {
			sb.WriteByte(',')
		}
	}
	for i := rem; i < len(digits); i += 3 {
		sb.WriteString(digits[i : i+3])
		if i+3 < len(digits) {
			sb.WriteByte(',')
		}
	}
	return sb.String()
}

func formatUsageBucket(sb *strings.Builder, label string, b *usageBucket) {
	sb.WriteString(fmt.Sprintf("[%s] calls: %d\n", label, b.calls))
	sb.WriteString(fmt.Sprintf("  prompt: %s | completion: %s | total: %s\n",
		formatThousands(b.promptTokens),
		formatThousands(b.completionTokens),
		formatThousands(b.totalTokens)))
	sb.WriteString(fmt.Sprintf("  cache: %s | elapsed: %s\n",
		formatThousands(b.cacheHitTokens),
		formatElapsed(b.elapsedMs)))
}

func formatElapsed(ms int64) string {
	if ms < 0 {
		return "unk"
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}
