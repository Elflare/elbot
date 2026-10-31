package builtin

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"elbot/internal/events"
	"elbot/internal/logging"
)

func formatLogEntries(category events.LogCategory, raw bool) func([]logging.LogEntry) string {
	title := "runtime logs"
	switch category {
	case events.LogAudit:
		title = "audit events"
	case events.LogElnis:
		title = "elwisp logs"
	}
	return func(entries []logging.LogEntry) string {
		if raw {
			return formatRawLogEntries(title, entries)
		}
		blocks := make([]string, 0, len(entries))
		for _, entry := range entries {
			blocks = append(blocks, formatLogEntry(category, entry))
		}
		return title + ":\n" + strings.Join(blocks, "\n---\n")
	}
}

func formatLogEntry(category events.LogCategory, entry logging.LogEntry) string {
	fields := make(map[string]any)
	body := entry.Content
	if entry.Type == events.ContentJSON {
		decoder := json.NewDecoder(strings.NewReader(entry.Content))
		decoder.UseNumber()
		var value any
		// LogEntry guarantees valid JSON; parsing here only builds the view.
		_ = decoder.Decode(&value)
		if object, ok := value.(map[string]any); ok {
			fields, body = object, ""
		} else {
			body = formatLogValue(value)
		}
	} else {
		for key, value := range entry.LegacyFields {
			fields[key] = value
		}
	}
	subject, status := entry.EventName, formatLogStatus(entry.ResultStatus)
	var details []string
	switch category {
	case events.LogAudit:
		subject, status, details = formatAuditFields(entry, fields)
	case events.LogElnis:
		if value, ok := fields["elwisp_name"]; ok && value != "" {
			subject = takeLogField(fields, "elwisp_name")
		}
	}
	header := formatLogEntryTime(entry)
	if category == events.LogRuntime {
		if entry.Module != "" {
			header += " [" + entry.Module + "]"
		}
		if entry.EventName != "" {
			header += " " + entry.EventName
		}
	} else if subject != "" {
		header += " [" + subject + "]"
	}
	if status != "" {
		header += " " + status
	}
	if ms, err := strconv.ParseInt(fmt.Sprint(fields["elapsed_ms"]), 10, 64); err == nil && ms >= 0 {
		header += " " + formatLogDuration(ms)
		delete(fields, "elapsed_ms")
	}
	if summary := strings.TrimRight(takeLogField(fields, "summary"), "\r\n"); summary != "" {
		header += " · " + summary
	}
	lines := []string{header}
	if body != "" {
		lines = append(lines, "["+strings.TrimRight(body, "\r\n")+"]")
	}
	if _, ok := fields["arguments"]; ok {
		lines = append(lines, takeLogField(fields, "arguments"))
	}
	bodyKeys := []string{"result", "content", "text", "before_text", "after_text", "source_text", "raw_text"}
	primaryCount := 0
	for _, key := range bodyKeys[:3] {
		if _, ok := fields[key]; ok {
			primaryCount++
		}
	}
	for _, key := range bodyKeys {
		if _, ok := fields[key]; ok {
			value := "[" + strings.TrimRight(takeLogField(fields, key), "\r\n") + "]"
			if strings.HasSuffix(key, "_text") || primaryCount > 1 {
				value = key + ":\n" + value
			}
			lines = append(lines, value)
		}
	}
	lines = append(lines, details...)
	if text := groupLogFields(fields, "error", "detail", "reason", "risk_reasons"); text != "" {
		lines = append(lines, text)
	}
	meta := ""
	if category != events.LogRuntime && subject != entry.EventName {
		meta = entry.EventName
	}
	if text := groupLogFields(fields, "actor_id", "run_source"); text != "" {
		if meta != "" {
			meta += " · "
		}
		meta += text
	}
	footer := groupLogFields(fields, "risk", "source")
	if entry.Level != "" {
		if footer != "" {
			footer = entry.Level + " · " + footer
		} else if meta != "" {
			meta += " · " + entry.Level
		} else {
			meta = entry.Level
		}
	}
	if meta != "" {
		lines = append(lines, meta)
	}
	for _, identity := range []struct {
		label string
		value *string
	}{
		{"session", entry.Identity.SessionID}, {"run_id", entry.Identity.RunID}, {"attempt", entry.Identity.Attempt},
		{"request_id", entry.Identity.RequestID}, {"root_request_id", entry.Identity.RootRequestID},
	} {
		if identity.value != nil {
			value := *identity.value
			if value == "" {
				value = `""`
			}
			lines = append(lines, identity.label+": "+value)
		}
	}
	if footer != "" {
		lines = append(lines, footer)
	}
	if entry.Type != events.ContentJSON {
		for _, key := range []string{"module", "type", "time", "level", "event", "result_status", "msg"} {
			delete(fields, key)
		}
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for len(keys) > 0 {
		n := min(3, len(keys))
		lines = append(lines, groupLogFields(fields, keys[:n]...))
		keys = keys[n:]
	}
	return strings.Join(lines, "\n")
}

func takeLogField(fields map[string]any, key string) string {
	value, ok := fields[key]
	delete(fields, key)
	if !ok {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return formatLogValue(value)
}

func groupLogFields(fields map[string]any, keys ...string) string {
	var parts []string
	for _, key := range keys {
		if _, ok := fields[key]; !ok {
			continue
		}
		label := key
		if key == "actor_id" {
			label = "actor"
		}
		value := takeLogField(fields, key)
		if value == "" {
			value = `""`
		}
		if strings.Contains(value, "\n") {
			parts = append(parts, label+":\n["+strings.TrimRight(value, "\r\n")+"]")
		} else {
			parts = append(parts, label+": "+value)
		}
	}
	return strings.Join(parts, " · ")
}

// formatLogValue renders decoded JSON without another encode/decode cycle.
func formatLogValue(value any) string {
	switch value := value.(type) {
	case string:
		if value == "" {
			return `""`
		}
		return value
	case nil:
		return "null"
	case []any:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			parts = append(parts, formatLogValue(item))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			parts = append(parts, key+": "+formatLogValue(value[key]))
		}
		return "{" + strings.Join(parts, " · ") + "}"
	default:
		return fmt.Sprint(value)
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

func formatLogStatus(status events.ResultStatus) string {
	switch status {
	case events.ResultSucceeded:
		return "✓"
	case events.ResultFailed:
		return "✗"
	case events.ResultCanceled:
		return "已取消"
	case events.ResultRejected:
		return "已拒绝"
	case events.ResultSkipped:
		return "已跳过"
	default:
		return ""
	}
}

func formatLogDuration(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%d.%03d", ms/1000, ms%1000), "0"), ".") + "s"
}
