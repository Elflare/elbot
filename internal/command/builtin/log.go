package builtin

import (
	"bytes"
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
		Usage:       "/audit [options]",
		Description: "Show recent audit events.",
		Help: strings.TrimSpace(`Options:
  -n, --limit <n>       Number of events to show. Default: 5.
  --days <n>            Read logs from the last n days. Default: 1.
  --level <level>       Minimum level: debug, info, warn, error. Default: debug.
  -d, -i, -w, -e       Shorthand for --level debug/info/warn/error.
  --raw                Show original physical log records.
  -t                   Filter tool events.
  --hook               Filter records from the hook module.
  --since <time>        Show events after a time, e.g. 2h, 30m, 2026-06-03, 2026-06-03T15:04:05.

  --until <time>        Show events before a time.
  --event <name>        Filter by event, e.g. tool_call, llm_usage, permission_denied.
  --risk <level>        Filter by risk.
  --actor <id>          Filter by actor_id.
  --session <id>        Filter by session_id.
  --tool <name>         Filter by tool.
  --msg <text>          Filter decoded log content.
  --contains <text>     Search decoded content and raw records.

Examples:
  /audit
  /audit --event tool_call --risk high -n 10
  /audit -d --contains "hello"
  /audit --actor cli:local --since 24h`),
	}
}

func NewAudit(deps Deps) command.Handler {
	return logCommand{deps: deps, info: auditInfo(), audit: true}
}

func elwispInfo() command.Info {
	return command.Info{
		Name:        "elwisp",
		Usage:       "/elwisp [name] [options]",
		Description: "Show recent Elnis/Elwisp logs.",
		Help: strings.TrimSpace(`Options:
  [name]                Filter by Elwisp name. Equivalent to --name.
  -n, --limit <n>       Number of log lines to show. Default: 5.
  --days <n>            Read logs from the last n days. Default: 1.
  --level <level>       Minimum level: debug, info, warn, error. Default: debug.
  -d, -i, -w, -e       Shorthand for --level debug/info/warn/error.
  --raw                Show original physical log records.
  --since <time>        Show logs after a time, e.g. 2h, 30m, 2026-06-03, 2026-06-03T15:04:05.
  --until <time>        Show logs before a time.
  --name <name>         Filter by Elwisp name.
  --elwisp <name>       Alias of --name.
  --source <source>     Filter by event source.
  --id <id>             Filter by external source id.
  --source-id <id>      Alias of --id.
  --mode <mode>         Filter by mode: record, direct, llm.
  --event-key <key>     Filter by Elnis event key.
  --event-id <id>       Filter by internal Elnis event id.
  --token <name>        Filter by Elnis token name.
  --tag <tag>           Filter by Elwisp tag. Can be repeated.
  --msg <text>          Filter decoded log content.
  --contains <text>     Search decoded content and raw records.

Examples:
  /elwisp
  /elwisp server-watchdog -n 20
  /elwisp --name server-watchdog --since 2h
  /elwisp --source minecraft-main --mode llm -w`),
	}
}

func NewElwisp(deps Deps) command.Handler {
	return logCommand{deps: deps, info: elwispInfo(), elwisp: true}
}

func logInfo() command.Info {
	return command.Info{
		Name:        "log",
		Usage:       "/log [options]",
		Description: "Show recent runtime logs.",
		Help: strings.TrimSpace(`Options:
  -n, --limit <n>       Number of log lines to show. Default: 5.
  --days <n>            Read logs from the last n days. Default: 1.
  --level <level>       Minimum level: debug, info, warn, error. Default: debug.
  -d, -i, -w, -e       Shorthand for --level debug/info/warn/error.
  --raw                Show original physical log records.
  -u, -a, -t           Filter user/assistant/tool events.
  -s, --system         Filter system prompt events.
  --hook               Filter records from the hook module.
  --since <time>        Show logs after a time, e.g. 2h, 30m, 2026-06-03, 2026-06-03T15:04:05.

  --until <time>        Show logs before a time.
  --msg <text>          Filter decoded log content.
  --contains <text>     Search decoded content and raw records.

Examples:
  /log
  /log -w -n 10
  /log --msg startup --days 3`),
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
		return formatCommandHelp(req.Prefix, c.info), nil
	}
	if c.audit {
		query, err := parseAuditQuery(req.Args)
		if err != nil {
			return nil, err
		}
		return queryLogs(ctx, c.deps, query, formatAuditEntries(query.Raw))
	}
	if c.elwisp {
		query, err := parseElwispLogQuery(req.Args)
		if err != nil {
			return nil, err
		}
		return queryLogs(ctx, c.deps, query, formatElwispLogEntries(query.Raw))
	}
	query, err := parseRuntimeLogQuery(req.Args)
	if err != nil {
		return nil, err
	}
	return queryLogs(ctx, c.deps, query, formatRuntimeLogEntries(query.Raw))
}

func (c logCommand) Complete(ctx context.Context, req command.CompletionRequest) []command.Completion {
	_ = ctx
	token := currentCompletionToken(req)
	previous := previousLogToken(req.Raw, token.Start)
	if previous == "--level" {
		return completeStringOptions([]string{"debug", "info", "warn", "error"}, token.Text, token.Start, token.End, "log_level")
	}
	if c.audit && previous == "--risk" {
		return completeStringOptions([]string{"low", "medium", "high", "critical"}, token.Text, token.Start, token.End, "risk")
	}
	if c.audit && previous == "--event" {
		return completeStringOptions([]string{"llm_usage", "llm_error", "session_naming_completed", "tool_call", "permission_denied", "session_resume", "session_fork", "hook_tool_call"}, token.Text, token.Start, token.End, "audit_event")
	}
	if c.audit && previous == "--tool" {
		return completeToolNames(c.deps, token.Text, token.Start, token.End)
	}
	if c.elwisp && previous == "--mode" {
		return completeStringOptions([]string{"record", "direct", "llm"}, token.Text, token.Start, token.End, "elnis_mode")
	}
	if strings.HasPrefix(token.Text, "-") || token.Text == "" {
		return completeStaticOptions(logCompletionOptions(c.audit, c.elwisp), token.Text, token.Start, token.End, "option")
	}
	return nil
}

func logCompletionOptions(audit, elwisp bool) []completionOption {
	options := []completionOption{
		{Text: "-n", Description: "Number of entries"},
		{Text: "--limit", Description: "Number of entries"},
		{Text: "--days", Description: "Read logs from recent days"},
		{Text: "--level", Description: "Minimum level"},
		{Text: "--raw", Description: "Original physical records"},
		{Text: "-d", Description: "Minimum debug level"},
		{Text: "-i", Description: "Info level"},
		{Text: "-w", Description: "Warn level"},
		{Text: "-e", Description: "Error level"},
		{Text: "--since", Description: "Show entries after time"},
		{Text: "--until", Description: "Show entries before time"},
		{Text: "--msg", Description: "Filter decoded content"},
		{Text: "--contains", Description: "Filter text fields"},
	}
	if !elwisp {
		options = append(options,
			completionOption{Text: "-t", Description: "Tool events"},
			completionOption{Text: "--hook", Description: "Hook module records"},
		)
	}
	if !audit && !elwisp {
		options = append(options,
			completionOption{Text: "-u", Description: "User events"},
			completionOption{Text: "-a", Description: "Assistant events"},
			completionOption{Text: "-s", Description: "System prompt events"},
			completionOption{Text: "--system", Description: "System prompt events"},
		)
	}
	if audit {
		options = append(options,
			completionOption{Text: "--event", Description: "Filter audit event"},
			completionOption{Text: "--risk", Description: "Filter risk level"},
			completionOption{Text: "--actor", Description: "Filter actor ID"},
			completionOption{Text: "--session", Description: "Filter session ID"},
			completionOption{Text: "--tool", Description: "Filter tool name"},
		)
	}
	if elwisp {
		options = append(options,
			completionOption{Text: "--name", Description: "Filter Elwisp name"},
			completionOption{Text: "--elwisp", Description: "Filter Elwisp name"},
			completionOption{Text: "--source", Description: "Filter event source"},
			completionOption{Text: "--tag", Description: "Filter Elwisp tag"},
			completionOption{Text: "--id", Description: "Filter source event ID"},
			completionOption{Text: "--source-id", Description: "Filter source event ID"},
			completionOption{Text: "--mode", Description: "Filter Elnis mode"},
			completionOption{Text: "--event-key", Description: "Filter event key"},
			completionOption{Text: "--event-id", Description: "Filter internal event ID"},
			completionOption{Text: "--token", Description: "Filter token name"},
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
		return &command.Result{Content: "log reader is not configured"}, nil
	}
	entries, err := deps.Logs.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	content := "no log entries found"
	if len(entries.Entries) > 0 {
		content = formatter(entries.Entries)
	}
	if entries.DamagedCount > 0 {
		content += fmt.Sprintf("\n本次扫描发现 %d 条损坏记录，查询结果可能不完整。", entries.DamagedCount)
		if query.Raw {
			for _, raw := range entries.DamagedLines {
				content += "\n损坏记录（无法判断是否匹配筛选条件）：\n" + raw
			}
			if entries.DamagedCount > len(entries.DamagedLines) {
				content += fmt.Sprintf("\n另有 %d 条损坏记录未展示。", entries.DamagedCount-len(entries.DamagedLines))
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
		case "risk":
			query.ContentFields["risk"] = value
		case "actor":
			query.ContentFields["actor_id"] = value
		case "session":
			fields["session_id"] = value
		case "tool":
			query.ContentFields["tool"] = value
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
		case "-t":
			if !opts.eventFilters {
				return fmt.Errorf("unknown option: %s", name)
			}
			fields["event"] = "tool_call"
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

func formatAuditEntries(raw bool) func([]logging.LogEntry) string {
	return func(entries []logging.LogEntry) string {
		if raw {
			return formatRawLogEntries("audit events", entries)
		}
		var sb strings.Builder
		sb.WriteString("audit events:\n")
		for i, entry := range entries {
			if i > 0 {
				sb.WriteString("\n\n")
			}
			result := "未声明"
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
			level := entry.Level
			if level == "" {
				level = "未声明"
			}
			fmt.Fprintf(&sb, "%s  %s  %s  %s\n", formatLogEntryTime(entry), level, entry.EventName, result)
			content := entry.Content
			if entry.Type == events.ContentJSON {
				var body bytes.Buffer
				if err := json.Indent(&body, []byte(content), "", "  "); err == nil {
					content = body.String()
				}
			}
			sb.WriteString(content)
			if entry.LegacyFields != nil {
				appendRuntimeLogFields(&sb, entry.LegacyFields)
			}
		}
		return sb.String()
	}
}

func formatRuntimeLogEntries(raw bool) func([]logging.LogEntry) string {
	return func(entries []logging.LogEntry) string {
		if raw {
			return formatRawLogEntries("runtime logs", entries)
		}
		var sb strings.Builder
		sb.WriteString("runtime logs:\n")
		for i, entry := range entries {
			if i > 0 {
				sb.WriteString("\n\n")
			}
			result := "未声明"
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
			level := entry.Level
			if level == "" {
				level = "未声明"
			}
			fmt.Fprintf(&sb, "%s  %s  %s  %s\n", formatLogEntryTime(entry), level, entry.EventName, result)
			content := entry.Content
			if entry.Type == events.ContentJSON {
				var body bytes.Buffer
				if err := json.Indent(&body, []byte(content), "", "  "); err == nil {
					content = body.String()
				}
			}
			sb.WriteString(content)
			if entry.LegacyFields != nil {
				appendRuntimeLogFields(&sb, entry.LegacyFields)
			}
		}
		return sb.String()
	}
}

func formatElwispLogEntries(raw bool) func([]logging.LogEntry) string {
	return func(entries []logging.LogEntry) string {
		if raw {
			return formatRawLogEntries("elwisp logs", entries)
		}
		var sb strings.Builder
		sb.WriteString("elwisp logs:\n")
		for i, entry := range entries {
			if i > 0 {
				sb.WriteString("\n\n")
			}
			result := "未声明"
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
			level := entry.Level
			if level == "" {
				level = "未声明"
			}
			fmt.Fprintf(&sb, "%s  %s  %s  %s\n", formatLogEntryTime(entry), level, entry.EventName, result)
			content := entry.Content
			if entry.Type == events.ContentJSON {
				var body bytes.Buffer
				if err := json.Indent(&body, []byte(content), "", "  "); err == nil {
					content = body.String()
				}
			}
			sb.WriteString(content)
			if entry.LegacyFields != nil {
				appendRuntimeLogFields(&sb, entry.LegacyFields)
			}
		}
		return sb.String()
	}
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
		return "unknown-time"
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
