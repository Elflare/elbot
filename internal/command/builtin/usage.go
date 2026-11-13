package builtin

import (
	"context"
	"elbot/internal/operationresult"
	"fmt"
	"strconv"
	"strings"

	command "elbot/internal/command/share"
	"elbot/internal/contextinfo"
	"elbot/internal/control"
	"elbot/internal/logging"
)

const usageQueryLimit = 100000

func usageInfo() command.Info {
	return command.Info{
		Name:        "usage",
		Args:        "[options]",
		Description: "用量统计",
		MinRole:     contextinfo.RoleSuperadmin,
		Help: `-d/--days <n>：天数，默认1；--by <model|day|session>：分组，默认model
-n <x[-y]>：筛选后按新到旧取第x次或x..y次，1≤x≤y≤100000
-m/--model <name>：模型；-s/--session <id>：会话
--since/--until <time>：起止时间，如 2h、2026-06-03`,
	}
}

func NewUsage(deps Deps) command.Handler {
	return usageCommand{deps: deps}
}

type usageCommand struct {
	deps Deps
}

func (c usageCommand) Info() command.Info { return usageInfo() }

func (c usageCommand) Handle(ctx context.Context, req command.Request) (command.Result, error) {
	if wantsCommandHelp(req.Args) {
		return formatCommandHelp(c.deps.CommandText, c.Info()), nil
	}
	query, groupBy, callRange, err := parseUsageQuery(req.Args)
	if err != nil {
		return command.Result{Status: operationresult.InvalidArguments, Message: err.Error()}, nil
	}
	first, last := 1, usageQueryLimit
	if callRange != nil {
		first, last = callRange.start, callRange.end
	}
	data, err := c.deps.Control.QueryUsage(ctx, control.QueryUsageRequest{Query: query, GroupBy: string(groupBy), First: first, Last: last})
	if err != nil {
		return command.Result{}, err
	}
	return command.FromOperationResult(data, command.ResultKindUsage), nil
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
