package builtin

import (
	"context"
	"elbot/internal/events"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"elbot/internal/command"
	"elbot/internal/logging"
)

const defaultLogListLimit = 5

func auditInfo() command.Info {
	return command.Info{
		Name:        "audit",
		Args:        "[options]",
		Description: "审计日志",
		Help: strings.TrimSpace(`-n, --limit <n>       条数，默认 5
--days <n>            天数，默认 1
--level <level>       最低 debug|info|warn|error，默认 debug
-d/-i/-w/-e           级别简写
--raw                原始记录
-t, --tool [name]     工具开始/结束，可指定名称
--run_source <name>   foreground|cron|elnis
--hook               Hook 日志
--since/--until <time>  时间范围，如 2h、2026-06-03
--event <name>        事件
--risk <level>        风险
--actor <id>          用户
--session <id>        会话
--msg <text>          正文过滤
--contains <text>     正文及原始记录搜索`),
	}
}

func NewAudit(deps Deps) command.Handler {
	return logCommand{deps: deps, info: auditInfo(), audit: true}
}

func elwispInfo() command.Info {
	return command.Info{
		Name:        "elwisp",
		Args:        "[name] [options]",
		Description: "Elwisp 日志",
		Help: strings.TrimSpace(`[name], --name/--elwisp <name>  Elwisp 名称
-n, --limit <n>       条数，默认 5
--days <n>            天数，默认 1
--level <level>       最低 debug|info|warn|error，默认 debug
-d/-i/-w/-e           级别简写
--raw                原始记录
--since/--until <time>  时间范围，如 2h、2026-06-03
--source <source>    来源
--id/--source-id <id> 外部来源 ID
--mode <mode>        record|direct|llm
--event-key <key>    事件 key
--event-id <id>      内部事件 ID
--token <name>       token 名
--tag <tag>          标签，可重复
--msg <text>         正文过滤
--contains <text>    正文及原始记录搜索`),
	}
}

func NewElwisp(deps Deps) command.Handler {
	return logCommand{deps: deps, info: elwispInfo(), elwisp: true}
}

func logInfo() command.Info {
	return command.Info{
		Name:        "log",
		Args:        "[options]",
		Description: "运行日志",
		Help: strings.TrimSpace(`-n, --limit <n>       条数，默认 5
--days <n>            天数，默认 1
--level <level>       最低 debug|info|warn|error，默认 debug
-d/-i/-w/-e           级别简写
--raw                原始记录
-u/-a                用户/助手消息
-s, --system         系统提示词
--hook               Hook 日志
--since/--until <time>  时间范围，如 2h、2026-06-03
--msg <text>          正文过滤
--contains <text>     正文及原始记录搜索`),
	}
}

func NewLog(deps Deps) command.Handler {
	return logCommand{deps: deps, info: logInfo()}
}

type logCommand struct {
	deps   Deps
	info   command.Info
	audit  bool
	elwisp bool
}

func (c logCommand) Info() command.Info { return c.info }

func (c logCommand) Handle(ctx context.Context, req command.Request) (*command.Result, error) {
	if wantsCommandHelp(req.Args) {
		return formatCommandHelp(c.deps.CommandText, c.info), nil
	}
	if c.audit {
		query, err := parseAuditQuery(req.Args)
		if err != nil {
			return nil, err
		}
		return queryLogs(ctx, c.deps, query, formatLogEntries("audit events", query.Raw))
	}
	if c.elwisp {
		query, err := parseElwispLogQuery(req.Args)
		if err != nil {
			return nil, err
		}
		return queryLogs(ctx, c.deps, query, formatLogEntries("elwisp logs", query.Raw))
	}
	query, err := parseRuntimeLogQuery(req.Args)
	if err != nil {
		return nil, err
	}
	return queryLogs(ctx, c.deps, query, formatLogEntries("runtime logs", query.Raw))
}

func (c logCommand) Complete(ctx context.Context, req command.CompletionRequest) []command.Completion {
	token := command.CurrentCompletionToken(req)
	previous := previousLogToken(req.Raw, token.Start)
	if c.audit && previous == "--run_source" {
		return completeStringOptions([]string{"foreground", "cron", "elnis"}, token.Text, token.Start, token.End, "run_source")
	}
	if previous == "--level" {
		return completeStringOptions([]string{"debug", "info", "warn", "error"}, token.Text, token.Start, token.End, "log_level")
	}
	if c.audit && previous == "--risk" {
		return completeStringOptions([]string{"low", "medium", "high", "critical"}, token.Text, token.Start, token.End, "risk")
	}
	if c.audit && previous == "--event" {
		return completeStringOptions([]string{"llm_usage", "llm_error", "session_naming_completed", "tool_call_prepared", "tool_call_completed", "permission_denied", "session_resume", "session_fork", "hook_tool_call_completed", "hook_tool_log"}, token.Text, token.Start, token.End, "audit_event")
	}
	if c.audit && previous == "--tool" && !strings.HasPrefix(token.Text, "-") {
		return completeToolNames(ctx, c.deps, token.Text, token.Start, token.End)
	}
	if c.elwisp && previous == "--mode" {
		return completeStringOptions([]string{"record", "direct", "llm"}, token.Text, token.Start, token.End, "elnis_mode")
	}
	if strings.HasPrefix(token.Text, "-") || token.Text == "" {
		return command.CompleteStaticOptions(logCompletionOptions(c.audit, c.elwisp), token.Text, token.Start, token.End, "option")
	}
	return nil
}

func logCompletionOptions(audit, elwisp bool) []command.CompletionOption {
	options := []command.CompletionOption{
		{Text: "-n", Description: "条数"},
		{Text: "--limit", Description: "条数"},
		{Text: "--days", Description: "天数"},
		{Text: "--level", Description: "最低级别"},
		{Text: "--raw", Description: "原始记录"},
		{Text: "-d", Description: "debug"},
		{Text: "-i", Description: "info"},
		{Text: "-w", Description: "warn"},
		{Text: "-e", Description: "error"},
		{Text: "--since", Description: "起始时间"},
		{Text: "--until", Description: "截止时间"},
		{Text: "--msg", Description: "正文过滤"},
		{Text: "--contains", Description: "文本搜索"},
	}
	if !elwisp {
		options = append(options,
			command.CompletionOption{Text: "--hook", Description: "Hook 日志"},
		)
	}
	if !audit && !elwisp {
		options = append(options,
			command.CompletionOption{Text: "-u", Description: "用户消息"},
			command.CompletionOption{Text: "-a", Description: "助手消息"},
			command.CompletionOption{Text: "-s", Description: "系统提示词"},
			command.CompletionOption{Text: "--system", Description: "系统提示词"},
		)
	}
	if audit {
		options = append(options,
			command.CompletionOption{Text: "-t", Description: "工具开始/结束"},
			command.CompletionOption{Text: "--run_source", Description: "来源: foreground/cron/elnis"},
			command.CompletionOption{Text: "--event", Description: "事件过滤"},
			command.CompletionOption{Text: "--risk", Description: "风险过滤"},
			command.CompletionOption{Text: "--actor", Description: "用户 ID"},
			command.CompletionOption{Text: "--session", Description: "会话 ID"},
			command.CompletionOption{Text: "--tool", Description: "工具名称"},
		)
	}
	if elwisp {
		options = append(options,
			command.CompletionOption{Text: "--name", Description: "Elwisp 名称"},
			command.CompletionOption{Text: "--elwisp", Description: "Elwisp 名称"},
			command.CompletionOption{Text: "--source", Description: "事件来源"},
			command.CompletionOption{Text: "--tag", Description: "Elwisp 标签"},
			command.CompletionOption{Text: "--id", Description: "来源事件 ID"},
			command.CompletionOption{Text: "--source-id", Description: "来源事件 ID"},
			command.CompletionOption{Text: "--mode", Description: "Elnis 模式"},
			command.CompletionOption{Text: "--event-key", Description: "事件 key"},
			command.CompletionOption{Text: "--event-id", Description: "内部事件 ID"},
			command.CompletionOption{Text: "--token", Description: "token 名"},
		)
	}
	return options
}

func previousLogToken(raw string, tokenStart int) string {
	before := strings.TrimSpace(raw[:tokenStart])
	if before == "" {
		return ""
	}
	fields := strings.Fields(before)
	if len(fields) == 0 {
		return ""
	}
	return fields[len(fields)-1]
}

func queryLogs(ctx context.Context, deps Deps, query logging.LogQuery, formatter func([]logging.LogEntry) string) (*command.Result, error) {
	if deps.Logs == nil {
		return &command.Result{Content: "logs: not configured"}, nil
	}
	entries, err := deps.Logs.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	content := "nil"
	if len(entries.Entries) > 0 {
		content = formatter(entries.Entries)
	}
	if entries.DamagedCount > 0 {
		content += fmt.Sprintf("\nwarn: damaged=%d · results incomplete", entries.DamagedCount)
		if query.Raw {
			for _, raw := range entries.DamagedLines {
				content += "\n损坏记录（无法判断是否匹配筛选条件）：\n" + raw
			}
			if entries.DamagedCount > len(entries.DamagedLines) {
				content += fmt.Sprintf("\ndamaged hidden=%d", entries.DamagedCount-len(entries.DamagedLines))
			}
		}
	}
	return &command.Result{Content: content}, nil
}

func parseAuditQuery(args string) (logging.LogQuery, error) {
	query := baseLogQuery("audit")
	query.MinLevel = "debug"
	fields := map[string]string{}
	if err := parseLogArgs(args, &query, fields, func(name, value string) error {
		switch name {
		case "event":
			fields["event"] = value
			query.Events = nil
		case "risk":
			query.ContentFields["risk"] = value
		case "actor":
			query.ContentFields["actor_id"] = value
		case "session":
			fields["session_id"] = value
		case "run_source":
			switch value {
			case "foreground", "cron", "elnis":
				query.ContentFields["run_source"] = value
			default:
				return fmt.Errorf("invalid --run_source %q: expected foreground, cron or elnis", value)
			}
		default:
			return fmt.Errorf("unknown option: --%s", name)
		}
		return nil
	}); err != nil {
		return query, err
	}
	query.Fields = fields
	return query, nil
}

func parseRuntimeLogQuery(args string) (logging.LogQuery, error) {
	query := baseLogQuery("elbot")
	query.MinLevel = "debug"
	fields := map[string]string{}
	if err := parseLogArgsWithOptions(args, &query, fields, func(name, value string) error {
		return fmt.Errorf("unknown option: --%s", name)
	}, logArgOptions{eventFilters: true, messageFilters: true, systemFilter: true}); err != nil {
		return query, err
	}
	query.Fields = fields
	return query, nil
}

func parseElwispLogQuery(args string) (logging.LogQuery, error) {
	query := baseLogQuery("elnis")
	query.MinLevel = "debug"
	fields := map[string]string{}
	setElwispName := func(value string) error {
		if query.ContentFields["elwisp_name"] != "" {
			return fmt.Errorf("elwisp name specified more than once")
		}
		query.ContentFields["elwisp_name"] = value
		return nil
	}
	if err := parseLogArgsWithOptions(args, &query, fields, func(name, value string) error {
		switch name {
		case "name", "elwisp":
			return setElwispName(value)
		case "source":
			query.ContentFields["source"] = value
		case "id", "source-id":
			query.ContentFields["source_id"] = value
		case "mode":
			query.ContentFields["mode"] = value
		case "event-key":
			query.ContentFields["event_key"] = value
		case "event-id":
			query.ContentFields["event_id"] = value
		case "token":
			query.ContentFields["token_name"] = value
		case "tag":
			query.ContentFieldContains["tags"] = append(query.ContentFieldContains["tags"], value)
		default:
			return fmt.Errorf("unknown option: --%s", name)
		}
		return nil
	}, logArgOptions{positional: setElwispName}); err != nil {
		return query, err
	}
	query.Fields = fields
	return query, nil
}

func baseLogQuery(prefix string) logging.LogQuery {
	return logging.LogQuery{Prefix: prefix, Limit: defaultLogListLimit, Days: 1, ContentFieldContains: map[string][]string{}, ContentFields: map[string]string{}}
}

func parseLogArgs(args string, query *logging.LogQuery, fields map[string]string, extra func(name, value string) error) error {
	return parseLogArgsWithOptions(args, query, fields, extra, logArgOptions{eventFilters: true})
}

type logArgOptions struct {
	eventFilters   bool
	messageFilters bool
	systemFilter   bool
	positional     func(value string) error
}

func parseLogArgsWithOptions(args string, query *logging.LogQuery, fields map[string]string, extra func(name, value string) error, opts logArgOptions) error {
	parts, err := splitLogArgs(args)
	if err != nil {
		return err
	}
	for i := 0; i < len(parts); i++ {
		name := parts[i]
		switch name {
		case "-n", "--limit":
			value, err := nextArg(parts, &i, name)
			if err != nil {
				return err
			}
			limit, err := parsePositiveInt(value, "limit")
			if err != nil {
				return err
			}
			query.Limit = limit
		case "--days":
			value, err := nextArg(parts, &i, name)
			if err != nil {
				return err
			}
			days, err := parsePositiveInt(value, "days")
			if err != nil {
				return err
			}
			query.Days = days
		case "--since":
			value, err := nextArg(parts, &i, name)
			if err != nil {
				return err
			}
			parsed, err := parseLogTime(value)
			if err != nil {
				return err
			}
			query.Since = &parsed
		case "--until":
			value, err := nextArg(parts, &i, name)
			if err != nil {
				return err
			}
			parsed, err := parseLogTime(value)
			if err != nil {
				return err
			}
			query.Until = &parsed
		case "--level":
			value, err := nextArg(parts, &i, name)
			if err != nil {
				return err
			}
			query.MinLevel = value
		case "-d":
			query.MinLevel = "debug"
		case "--raw":
			query.Raw = true
		case "-i":
			query.MinLevel = "info"
		case "-w":
			query.MinLevel = "warn"
		case "-e":
			query.MinLevel = "error"
		case "-u":
			if !opts.messageFilters {
				return fmt.Errorf("unknown option: %s", name)
			}
			fields["event"] = "user_message"
		case "-a":
			if !opts.messageFilters {
				return fmt.Errorf("unknown option: %s", name)
			}
			fields["event"] = "assistant_message"
		case "-t", "--tool":
			if !opts.eventFilters || opts.messageFilters {
				return fmt.Errorf("unknown option: %s", name)
			}
			if name == "--tool" && i+1 < len(parts) && !strings.HasPrefix(parts[i+1], "-") {
				i++
				query.ContentFields["tool"] = parts[i]
				continue
			}
			delete(fields, "event")
			query.Events = []string{"tool_call_prepared", "tool_call_completed"}
		case "-s", "--system":
			if !opts.systemFilter {
				return fmt.Errorf("unknown option: %s", name)
			}
			fields["event"] = "system_message"
		case "--hook":
			if !opts.eventFilters {
				return fmt.Errorf("unknown option: %s", name)
			}
			fields["module"] = "hook"
		case "--msg":
			value, err := nextArg(parts, &i, name)
			if err != nil {
				return err
			}
			query.ContentContains = value
		case "--contains":
			value, err := nextArg(parts, &i, name)
			if err != nil {
				return err
			}
			query.Contains = value
		default:
			if strings.HasPrefix(name, "--") {
				value, err := nextArg(parts, &i, name)
				if err != nil {
					return err
				}
				if err := extra(strings.TrimPrefix(name, "--"), value); err != nil {
					return err
				}
				continue
			}
			if strings.HasPrefix(name, "-") {
				return fmt.Errorf("unknown option: %s", name)
			}
			if opts.positional != nil {
				if err := opts.positional(name); err != nil {
					return err
				}
				continue
			}
			return fmt.Errorf("unexpected argument: %s", name)
		}
	}
	return nil
}

func splitLogArgs(args string) ([]string, error) {
	parts := []string{}
	var current strings.Builder
	var quote rune
	escaped := false
	for _, r := range args {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
				continue
			}
			current.WriteRune(r)
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			continue
		}
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			if current.Len() > 0 {
				parts = append(parts, current.String())
				current.Reset()
			}
			continue
		}
		current.WriteRune(r)
	}
	if escaped {
		current.WriteRune('\\')
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quoted argument")
	}
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}
	return parts, nil
}

func nextArg(fields []string, index *int, option string) (string, error) {
	if *index+1 >= len(fields) {
		return "", fmt.Errorf("%s requires a value", option)
	}
	*index = *index + 1
	return fields[*index], nil
}

func parsePositiveInt(value, name string) (int, error) {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 {
		return 0, fmt.Errorf("%s must be a positive number", name)
	}
	return parsed, nil
}

func parseLogTime(value string) (time.Time, error) {
	if d, err := time.ParseDuration(value); err == nil {
		return time.Now().Add(-d), nil
	}
	layouts := []string{"2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02"}
	for _, layout := range layouts {
		if parsed, err := time.ParseInLocation(layout, value, time.Local); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid time: %s", value)
}

func formatLogEntries(title string, raw bool) func([]logging.LogEntry) string {
	return func(entries []logging.LogEntry) string {
		if raw {
			return formatRawLogEntries(title, entries)
		}
		var sb strings.Builder
		sb.WriteString(title + ":\n")
		for i, entry := range entries {
			if i > 0 {
				sb.WriteString("\n\n")
			}
			result := ""
			switch entry.ResultStatus {
			case events.ResultSucceeded:
				result = "成功"
			case events.ResultFailed:
				result = "失败"
			case events.ResultCanceled:
				result = "已取消"
			case events.ResultRejected:
				result = "已拒绝"
			case events.ResultSkipped:
				result = "已跳过"
			}
			fmt.Fprintf(&sb, "%s  %s  %s", formatLogEntryTime(entry), entry.Level, entry.EventName)
			if result != "" {
				sb.WriteString("  " + result)
			}
			sb.WriteByte('\n')
			sb.WriteString(formatLogContent(entry))
			if entry.LegacyFields != nil {
				appendRuntimeLogFields(&sb, entry.LegacyFields)
			}
		}
		return sb.String()
	}
}

// formatLogContent renders JSON fields in source order. Strings are decoded
// once, so real newlines display normally while literal backslashes survive.
func formatLogContent(entry logging.LogEntry) string {
	if entry.Type != events.ContentJSON {
		return entry.Content
	}
	decoder := json.NewDecoder(strings.NewReader(entry.Content))
	decoder.UseNumber()
	var render func(string) (string, error)
	render = func(indent string) (string, error) {
		value, err := decoder.Token()
		if err != nil {
			return "", err
		}
		switch value := value.(type) {
		case json.Delim:
			var body strings.Builder
			for decoder.More() {
				if body.Len() > 0 {
					body.WriteString("\n" + indent)
				}
				if value == '{' {
					key, err := decoder.Token()
					if err != nil {
						return "", err
					}
					body.WriteString(key.(string) + ": ")
				} else {
					body.WriteString("- ")
				}
				content, err := render(indent + "  ")
				if err != nil {
					return "", err
				}
				if value == '{' && strings.Contains(content, "\n") {
					body.WriteString("\n" + indent + "  ")
				}
				body.WriteString(content)
			}
			if _, err := decoder.Token(); err != nil {
				return "", err
			}
			if body.Len() == 0 {
				if value == '{' {
					return "{}", nil
				}
				return "[]", nil
			}
			return body.String(), nil
		case string:
			return strings.ReplaceAll(value, "\n", "\n"+indent), nil
		case nil:
			return "null", nil
		default:
			return fmt.Sprint(value), nil
		}
	}
	content, err := render("")
	if err != nil {
		return entry.Content
	}
	return content
}

func appendRuntimeLogFields(sb *strings.Builder, fields map[string]string) {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		switch key {
		case "time", "level", "event", "type", "result_status", "msg":
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		appendField(sb, key, fields[key])
	}
}

func formatRawLogEntries(title string, entries []logging.LogEntry) string {
	var sb strings.Builder
	sb.WriteString(title)
	sb.WriteString(":\n")
	for i, entry := range entries {
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString("  ")
		sb.WriteString(entry.Raw)
	}
	return sb.String()
}

func formatLogEntryTime(entry logging.LogEntry) string {
	if entry.Time.IsZero() {
		return "unk-time"
	}
	return entry.Time.Format("2006-01-02 15:04:05")
}

func appendField(sb *strings.Builder, label, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	sb.WriteString(fmt.Sprintf(" %s=%s", label, value))
}

type LogModule struct{}

func (LogModule) RegisterCommands(registrar Registrar, deps Deps) error {
	return RegisterFactories(registrar, deps,
		NewLog,
		NewAudit,
		NewElwisp,
		NewUsage,
	)
}
