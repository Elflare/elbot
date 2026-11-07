package share

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	command "elbot/internal/command/share"
	"elbot/internal/events"
)

func formatLogs(d command.LogsData) string {
	var out strings.Builder
	if d.Title != "" && len(d.Items) > 0 {
		fmt.Fprintf(&out, "%s:\n", d.Title)
	}
	for i, item := range d.Items {
		if item.Raw != "" {
			if i > 0 {
				out.WriteByte('\n')
			}
			fmt.Fprintf(&out, "  %s", item.Raw)
			continue
		}
		if i > 0 {
			out.WriteString("\n---\n")
		}
		out.WriteString(formatLogDataItem(item))
	}
	if len(d.Items) == 0 {
		out.WriteString("nil")
	}
	if d.DamagedCount > 0 {
		if out.Len() > 0 {
			out.WriteByte('\n')
		}
		fmt.Fprintf(&out, "warn: damaged=%d · results incomplete", d.DamagedCount)
		for _, raw := range d.DamagedLines {
			fmt.Fprintf(&out, "\n损坏记录（无法判断是否匹配筛选条件）：\n%s", raw)
		}
		if hidden := d.DamagedCount - len(d.DamagedLines); hidden > 0 {
			fmt.Fprintf(&out, "\ndamaged hidden=%d", hidden)
		}
	}
	return strings.TrimRight(out.String(), "\n")
}

func formatLogDataItem(item command.LogData) string {
	fields, body := decodeLogContent(item)
	subject, status := item.EventName, logStatus(item.ResultStatus)
	var details []string
	if item.Category == events.LogAudit {
		subject, status, details = formatAuditDataFields(item, fields)
	} else if item.Category == events.LogElnis {
		if value, ok := fields["elwisp_name"]; ok && value != nil && value != "" {
			subject = takeLogDataField(fields, "elwisp_name")
		}
	}
	header := formatLogDataTime(item)
	if item.Category == events.LogRuntime {
		if item.Module != "" {
			header += " [" + item.Module + "]"
		}
		if item.EventName != "" {
			header += " " + item.EventName
		}
	} else if subject != "" {
		header += " [" + subject + "]"
	}
	if status != "" {
		header += " " + status
	}
	if elapsed, ok := logDataInt(fields["elapsed_ms"]); ok && elapsed >= 0 {
		header += " " + formatLogDataDuration(elapsed)
		delete(fields, "elapsed_ms")
	}
	if summary := strings.TrimRight(takeLogDataField(fields, "summary"), "\r\n"); summary != "" {
		header += " · " + summary
	}
	lines := []string{header}
	if body != "" {
		lines = append(lines, "["+strings.TrimRight(body, "\r\n")+"]")
	}
	if _, ok := fields["arguments"]; ok {
		lines = append(lines, takeLogDataField(fields, "arguments"))
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
			value := "[" + strings.TrimRight(takeLogDataField(fields, key), "\r\n") + "]"
			if strings.HasSuffix(key, "_text") || primaryCount > 1 {
				value = key + ":\n" + value
			}
			lines = append(lines, value)
		}
	}
	lines = append(lines, details...)
	if text := groupLogDataFields(fields, "error", "detail", "reason", "risk_reasons"); text != "" {
		lines = append(lines, text)
	}
	meta := ""
	if item.Category != events.LogRuntime && subject != item.EventName && !(item.EventName == "tool_call_completed" && status != "") {
		meta = item.EventName
	}
	if text := groupLogDataFields(fields, "actor", "run_source"); text != "" {
		if meta != "" {
			meta += " · "
		}
		meta += text
	}
	footer := groupLogDataFields(fields, "risk", "source")
	if item.Level != "" {
		if footer != "" {
			footer = item.Level + " · " + footer
		} else if meta != "" {
			meta += " · " + item.Level
		} else {
			meta = item.Level
		}
	}
	if meta != "" {
		lines = append(lines, meta)
	}
	lines = append(lines, "session: "+item.Identity.SessionID)
	if footer != "" {
		lines = append(lines, footer)
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for len(keys) > 0 {
		n := min(3, len(keys))
		lines = append(lines, groupLogDataFields(fields, keys[:n]...))
		keys = keys[n:]
	}
	return strings.Join(lines, "\n")
}

func decodeLogContent(item command.LogData) (map[string]any, string) {
	fields := map[string]any{}
	decoder := json.NewDecoder(bytes.NewReader(item.Content))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return fields, string(item.Content)
	}
	if object, ok := value.(map[string]any); ok {
		return object, ""
	}
	if text, ok := value.(string); ok {
		return fields, text
	}
	return fields, formatLogDataValue(value)
}

func formatAuditDataFields(item command.LogData, fields map[string]any) (subject, status string, details []string) {
	subject = item.EventName
	for _, key := range []string{"tool", "model", "job", "elwisp_name", "path", "name", "api"} {
		if value, ok := fields[key]; ok && value != nil && value != "" {
			subject = takeLogDataField(fields, key)
			if key == "model" {
				subject = strings.Trim(takeLogDataField(fields, "provider")+"/"+subject, "/")
			}
			break
		}
	}
	status = logStatus(item.ResultStatus)
	if (strings.HasSuffix(item.EventName, "_prepared") || strings.HasSuffix(item.EventName, "_started")) && (item.ResultStatus == events.ResultSucceeded || item.ResultStatus == events.ResultUnspecified) {
		status = "→"
	} else if item.EventName == "risk_confirmation_wait" {
		status = "待确认"
	}
	if item.EventName == "llm_usage" || item.EventName == "llm_error" {
		details = append(details, formatAuditDataUsage(fields))
	}
	if _, ok := fields["source_platform"]; ok {
		details = append(details, "route: "+takeLogDataField(fields, "source_platform")+" → "+takeLogDataField(fields, "target_platforms"))
	}
	for _, key := range []string{"tool_call_id", "module", "type", "time", "level", "event", "result_status", "msg"} {
		delete(fields, key)
	}
	return subject, status, details
}

func formatAuditDataUsage(fields map[string]any) string {
	token := func(key string) string {
		value, ok := fields[key]
		delete(fields, key)
		if !ok || value == nil {
			return "?"
		}
		return formatLogDataValue(value)
	}
	percent := "?"
	if value, ok := fields["cache_hit_percent"]; ok {
		if parsed, err := strconv.ParseFloat(fmt.Sprint(value), 64); err == nil {
			percent = fmt.Sprintf("%.1f%%", parsed)
		}
	}
	delete(fields, "cache_hit_percent")
	delete(fields, "total_tokens")
	delete(fields, "cache_hit_tokens")
	return "输入 " + token("prompt_tokens") + " · 输出 " + token("completion_tokens") + " · 缓存 " + percent
}

func takeLogDataField(fields map[string]any, key string) string {
	value, ok := fields[key]
	delete(fields, key)
	if !ok {
		return ""
	}
	if text, ok := value.(string); ok {
		if text == "" {
			return `""`
		}
		return text
	}
	return formatLogDataValue(value)
}

func groupLogDataFields(fields map[string]any, keys ...string) string {
	var parts []string
	for _, key := range keys {
		if value, ok := fields[key]; ok {
			delete(fields, key)
			text := formatLogDataValue(value)
			label := key
			if strings.Contains(text, "\n") {
				parts = append(parts, label+":\n["+strings.TrimRight(text, "\r\n")+"]")
			} else {
				parts = append(parts, label+": "+text)
			}
		}
	}
	return strings.Join(parts, " · ")
}

func formatLogDataValue(value any) string {
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
			parts = append(parts, formatLogDataValue(item))
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
			parts = append(parts, key+": "+formatLogDataValue(value[key]))
		}
		return "{" + strings.Join(parts, " · ") + "}"
	default:
		return fmt.Sprint(value)
	}
}

func logDataInt(value any) (int64, bool) {
	text := fmt.Sprint(value)
	if text == "<nil>" {
		return 0, false
	}
	n, err := strconv.ParseInt(text, 10, 64)
	return n, err == nil
}

func formatLogDataTime(item command.LogData) string {
	if item.Time.IsZero() {
		return "unk-time"
	}
	return item.Time.Format("2006-01-02 15:04:05")
}

func formatLogDataDuration(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%d.%03d", ms/1000, ms%1000), "0"), ".") + "s"
}

func logStatus(status events.ResultStatus) string {
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
