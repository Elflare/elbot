package builtin

import (
	"elbot/internal/events"
	"elbot/internal/logging"
	"fmt"
	"strconv"
	"strings"
)

func formatAuditFields(entry logging.LogEntry, fields map[string]any) (subject, status string, details []string) {
	subject = entry.EventName
	for _, key := range []string{"tool", "model", "job", "elwisp_name", "path", "name", "api"} {
		if value, ok := fields[key]; ok && value != "" {
			subject = takeLogField(fields, key)
			if key == "model" {
				subject = strings.Trim(takeLogField(fields, "provider")+"/"+subject, "/")
			}
			break
		}
	}
	status = formatLogStatus(entry.ResultStatus)
	if (strings.HasSuffix(entry.EventName, "_prepared") || strings.HasSuffix(entry.EventName, "_started")) && (entry.ResultStatus == events.ResultSucceeded || entry.ResultStatus == events.ResultUnspecified) {
		status = "→"
	} else if entry.EventName == "risk_confirmation_wait" {
		status = "待确认"
	}
	if entry.EventName == "llm_usage" || entry.EventName == "llm_error" {
		details = append(details, formatAuditUsage(fields))
	}
	if _, ok := fields["source_platform"]; ok {
		details = append(details, "route: "+takeLogField(fields, "source_platform")+" → "+takeLogField(fields, "target_platforms"))
	}
	for _, key := range []string{"tool_call_id", "module", "type", "time", "level", "event", "result_status", "msg"} {
		delete(fields, key)
	}
	return subject, status, details
}

func formatAuditUsage(fields map[string]any) string {
	token := func(key string) string {
		value := fields[key]
		delete(fields, key)
		if value == nil {
			return "?"
		}
		return formatLogValue(value)
	}
	percent := "?"
	if value, err := strconv.ParseFloat(fmt.Sprint(fields["cache_hit_percent"]), 64); err == nil {
		percent = fmt.Sprintf("%.1f%%", value)
	}
	delete(fields, "cache_hit_percent")
	delete(fields, "total_tokens")
	delete(fields, "cache_hit_tokens")
	return "输入 " + token("prompt_tokens") + " · 输出 " + token("completion_tokens") + " · 缓存 " + percent
}
