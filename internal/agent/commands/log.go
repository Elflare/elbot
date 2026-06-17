package commands

import (
	"context"
	"fmt"
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
  -d, -i, -w, -e       Shorthand for --level debug/info/warn/error. -d also shows raw entries.
  -u, -a, -t           Filter user/assistant/tool events.
  --hook               Filter hook events.
  --since <time>        Show events after a time, e.g. 2h, 30m, 2026-06-03, 2026-06-03T15:04:05.

  --until <time>        Show events before a time.
  --event <name>        Filter by event, e.g. tool_call, llm_usage, permission_denied.
  --risk <level>        Filter by risk.
  --actor <id>          Filter by actor_id.
  --session <id>        Filter by session_id.
  --tool <name>         Filter by tool.
  --msg <text>          Filter by msg field.
  --contains <text>     Filter by text/arguments/result/raw fields.

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

func logInfo() command.Info {
	return command.Info{
		Name:        "log",
		Usage:       "/log [options]",
		Description: "Show recent runtime logs.",
		Help: strings.TrimSpace(`Options:
  -n, --limit <n>       Number of log lines to show. Default: 5.
  --days <n>            Read logs from the last n days. Default: 1.
  --level <level>       Minimum level: debug, info, warn, error. Default: debug.
  -d, -i, -w, -e       Shorthand for --level debug/info/warn/error. -d also shows raw entries.
  -u, -a, -t           Filter user/assistant/tool events.
  --hook               Filter hook events.
  --since <time>        Show logs after a time, e.g. 2h, 30m, 2026-06-03, 2026-06-03T15:04:05.

  --until <time>        Show logs before a time.
  --msg <text>          Filter by msg field.
  --contains <text>     Filter by text/arguments/result/raw fields.

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
	deps  Deps
	info  command.Info
	audit bool
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
		return completeStringOptions([]string{"user_input", "assistant_output", "llm_usage", "tool_call", "permission_denied", "session_resume", "session_fork", "hook"}, token.Text, token.Start, token.End, "audit_event")
	}
	if c.audit && previous == "--tool" {
		return completeToolNames(c.deps, token.Text, token.Start, token.End)
	}
	if strings.HasPrefix(token.Text, "-") || token.Text == "" {
		return completeStaticOptions(logCompletionOptions(c.audit), token.Text, token.Start, token.End, "option")
	}
	return nil
}

func logCompletionOptions(audit bool) []completionOption {
	options := []completionOption{
		{Text: "-n", Description: "Number of entries"},
		{Text: "--limit", Description: "Number of entries"},
		{Text: "--days", Description: "Read logs from recent days"},
		{Text: "--level", Description: "Minimum level"},
		{Text: "-d", Description: "Debug level and raw entries"},
		{Text: "-i", Description: "Info level"},
		{Text: "-w", Description: "Warn level"},
		{Text: "-e", Description: "Error level"},
		{Text: "-u", Description: "User events"},
		{Text: "-a", Description: "Assistant events"},
		{Text: "-t", Description: "Tool events"},
		{Text: "--hook", Description: "Hook events"},
		{Text: "--since", Description: "Show entries after time"},
		{Text: "--until", Description: "Show entries before time"},
		{Text: "--msg", Description: "Filter msg field"},
		{Text: "--contains", Description: "Filter text fields"},
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
		return &command.Result{Content: "log reader is not configured\n"}, nil
	}
	entries, err := deps.Logs.QueryLogs(ctx, query)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return &command.Result{Content: "no log entries found\n"}, nil
	}
	return &command.Result{Content: formatter(entries)}, nil
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
			fields["risk"] = value
		case "actor":
			fields["actor_id"] = value
		case "session":
			fields["session_id"] = value
		case "tool":
			fields["tool"] = value
		default:
			return fmt.Errorf("unknown option: --%s", name)
		}
		return nil
	}); err != nil {
		return query, err
	}
	if fields["event"] == "user_input" {
		fields["event"] = "user_message"
	}
	if fields["event"] == "assistant_output" {
		fields["event"] = "assistant_message"
	}

	if len(query.FieldExists) > 0 {
		query.FieldExists = nil
		fields["event"] = "hook"
	}
	query.Fields = fields
	return query, nil
}

func parseRuntimeLogQuery(args string) (logging.LogQuery, error) {
	query := baseLogQuery("elbot")
	query.MinLevel = "debug"
	fields := map[string]string{}
	if err := parseLogArgs(args, &query, fields, func(name, value string) error {
		return fmt.Errorf("unknown option: --%s", name)
	}); err != nil {
		return query, err
	}
	query.Fields = fields
	return query, nil
}

func baseLogQuery(prefix string) logging.LogQuery {
	return logging.LogQuery{Prefix: prefix, Limit: defaultLogListLimit, Days: 1}
}

func parseLogArgs(args string, query *logging.LogQuery, fields map[string]string, extra func(name, value string) error) error {
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
			query.Raw = strings.EqualFold(value, "debug")
		case "-d":
			query.MinLevel = "debug"
			query.Raw = true
		case "-i":
			query.MinLevel = "info"
		case "-w":
			query.MinLevel = "warn"
		case "-e":
			query.MinLevel = "error"
		case "-u":
			fields["event"] = "user_message"
		case "-a":
			fields["event"] = "assistant_message"
		case "-t":
			fields["event"] = "tool_call"
		case "--hook":
			query.FieldExists = append(query.FieldExists, "hook")
		case "--msg":
			value, err := nextArg(parts, &i, name)
			if err != nil {
				return err
			}
			query.MsgContains = value
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
		for _, entry := range entries {
			f := entry.Fields
			sb.WriteString(fmt.Sprintf("  %s %s", formatLogEntryTime(entry), fieldOr(f, "event", entry.Message)))
			appendField(&sb, "session", f["session_id"])
			appendField(&sb, "actor", f["actor_id"])
			appendField(&sb, "tool", f["tool"])
			appendField(&sb, "risk", f["risk"])
			appendField(&sb, "provider", f["provider"])
			appendField(&sb, "model", f["model"])
			appendField(&sb, "elapsed_ms", f["elapsed_ms"])
			appendLLMUsageFields(&sb, f)
			appendField(&sb, "action", f["action"])
			appendField(&sb, "args", f["arguments"])
			appendField(&sb, "result", f["result"])
			appendField(&sb, "text", f["text"])
			appendField(&sb, "reason", f["reason"])
			appendField(&sb, "error", f["error"])
			sb.WriteString("\n")
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
		for _, entry := range entries {
			sb.WriteString(fmt.Sprintf("  %s %s %s", formatLogEntryTime(entry), strings.ToLower(entry.Level), entry.Message))
			appendRuntimeLogFields(&sb, entry.Fields)
			sb.WriteString("\n")
		}
		return sb.String()
	}
}

func appendLLMUsageFields(sb *strings.Builder, fields map[string]string) {
	prompt := strings.TrimSpace(fields["prompt_tokens"])
	completion := strings.TrimSpace(fields["completion_tokens"])
	total := strings.TrimSpace(fields["total_tokens"])
	if prompt != "" || completion != "" || total != "" {
		sb.WriteString(" tokens=")
		sb.WriteString(firstNonEmptyLogField(prompt, "?"))
		sb.WriteString("/")
		sb.WriteString(firstNonEmptyLogField(completion, "?"))
		sb.WriteString("/")
		sb.WriteString(firstNonEmptyLogField(total, "?"))
	}
	cacheHit := strings.TrimSpace(fields["cache_hit_tokens"])
	if cacheHit == "" {
		return
	}
	sb.WriteString(" cache_hit=")
	sb.WriteString(cacheHit)
	if prompt == "" {
		return
	}
	cacheHitTokens, errHit := strconv.Atoi(cacheHit)
	promptTokens, errPrompt := strconv.Atoi(prompt)
	if errHit != nil || errPrompt != nil || promptTokens <= 0 {
		return
	}
	sb.WriteString(fmt.Sprintf("/%d(%.1f%%)", promptTokens, float64(cacheHitTokens)*100/float64(promptTokens)))
}

func firstNonEmptyLogField(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func appendRuntimeLogFields(sb *strings.Builder, fields map[string]string) {
	for _, key := range []string{
		"event", "point", "hook", "priority", "order", "mode",
		"hook_point", "hook_mode",
		"kind", "name", "platform",
		"session_id", "request_id", "request_kind", "request_phase",
		"actor_id", "actor_role",
		"provider", "model", "tool", "risk",
		"text", "raw_text", "first_system_message_json", "arguments", "result", "success", "error",
	} {
		appendField(sb, key, fields[key])
	}
}

func formatRawLogEntries(title string, entries []logging.LogEntry) string {
	var sb strings.Builder
	sb.WriteString(title)
	sb.WriteString(":\n")
	for _, entry := range entries {
		sb.WriteString("  ")
		sb.WriteString(formatDebugLogEntry(entry))
		sb.WriteString("\n")
	}
	return sb.String()
}

func formatDebugLogEntry(entry logging.LogEntry) string {
	body := strings.TrimSpace(entry.Fields["body_json"])
	if body == "" {
		return entry.Raw
	}
	line := strings.TrimSpace(entry.Raw)
	if idx := strings.Index(line, " body_json="); idx >= 0 {
		line = line[:idx]
	}
	return line + "\n    body_json:\n" + indentDebugBlock(body, "      ")
}

func indentDebugBlock(text, prefix string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

func isDebugQuery(minLevel string) bool {
	return strings.EqualFold(strings.TrimSpace(minLevel), "debug")
}

func formatLogEntryTime(entry logging.LogEntry) string {
	if entry.Time.IsZero() {
		return "unknown-time"
	}
	return entry.Time.Format("2006-01-02 15:04:05")
}

func fieldOr(fields map[string]string, key, fallback string) string {
	if value := fields[key]; value != "" {
		return value
	}
	return fallback
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
	)
}
