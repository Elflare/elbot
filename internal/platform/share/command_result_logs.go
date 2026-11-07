package share

import (
	"encoding/json"
	"fmt"
	"sort"
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
	var fields map[string]any
	if item.Type == events.ContentJSON {
		decoder := json.NewDecoder(strings.NewReader(string(item.Content)))
		decoder.UseNumber()
		_ = decoder.Decode(&fields)
	}
	if fields == nil {
		fields = map[string]any{}
	}
	subject, status := item.EventName, logStatus(item.ResultStatus)
	if item.Category == events.LogElnis {
		if value, ok := fields["elwisp_name"]; ok {
			subject = fmt.Sprint(value)
			delete(fields, "elwisp_name")
		}
	}
	var out strings.Builder
	if item.Time.IsZero() {
		out.WriteString("unk-time")
	} else {
		out.WriteString(item.Time.Format("2006-01-02 15:04:05"))
	}
	if item.Category == events.LogRuntime {
		if item.Module != "" {
			fmt.Fprintf(&out, " [%s]", item.Module)
		}
		if item.EventName != "" {
			fmt.Fprintf(&out, " %s", item.EventName)
		}
	} else if subject != "" {
		fmt.Fprintf(&out, " [%s]", subject)
	}
	if status != "" {
		fmt.Fprintf(&out, " %s", status)
	}
	if body, ok := fields["summary"].(string); ok && body != "" {
		fmt.Fprintf(&out, " · %s", strings.TrimRight(body, "\r\n"))
		delete(fields, "summary")
	}
	if item.Type != events.ContentJSON && len(item.Content) > 0 {
		fmt.Fprintf(&out, "\n[%s]", strings.TrimRight(string(item.Content), "\r\n"))
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(&out, "\n%s: %s", key, formatLogDataValue(fields[key]))
	}
	fmt.Fprintf(&out, "\nsession: %s", item.Identity.SessionID)
	return out.String()
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

func formatLogDataValue(value any) string {
	switch value := value.(type) {
	case string:
		return value
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
	case []any:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			parts = append(parts, formatLogDataValue(item))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	default:
		return fmt.Sprint(value)
	}
}
