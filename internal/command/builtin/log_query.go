package builtin

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"elbot/internal/logging"
)

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
