package logging

import (
	"encoding/json"
	"fmt"
	"time"
	"unicode/utf8"

	"elbot/internal/events"
)

const (
	defaultContentLimit = 256
	maxRecordBytes      = 64 * 1024
	truncatedMarker     = "…"
)

// TruncateContent bounds log-only text to a positive Unicode character limit,
// including the ellipsis. Callers supply limit > 0.
func TruncateContent(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	return string(runes[:limit-1]) + truncatedMarker
}

func formatRecord(record events.LogRecord, at time.Time) ([]byte, error) {
	var value any = record.Content
	switch record.Type {
	case events.ContentText:
		limit := record.ContentLimit
		if limit == 0 {
			limit = defaultContentLimit
		}
		value = TruncateContent(record.Content, limit)
	case events.ContentJSON:
		// Preserve the source's field order and values when encoding the record.
		value = json.RawMessage(record.Content)
	}
	fields := map[string]any{
		"time":            at.Format(time.RFC3339),
		"category":        record.Category.String(),
		"level":           record.Level.String(),
		"event":           record.EventName,
		"module":          record.Module,
		"type":            record.Type.String(),
		"content":         value,
		"session_id":      record.Identity.SessionID,
		"run_id":          record.Identity.RunID,
		"attempt":         record.Identity.Attempt,
		"request_id":      record.Identity.RequestID,
		"root_request_id": record.Identity.RootRequestID,
	}
	if record.ResultStatus != events.ResultUnspecified {
		fields["result_status"] = record.ResultStatus.String()
	}
	data, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("format log record: %w", err)
	}
	if len(data)+1 > maxRecordBytes {
		return nil, fmt.Errorf("format log record: exceeds %d bytes including newline", maxRecordBytes)
	}
	return append(data, '\n'), nil
}

// decodeRecord validates the JSONL envelope; malformed lines are counted, not returned as I/O errors.
func decodeRecord(raw string) (LogEntry, bool) {
	var fields map[string]json.RawMessage
	var entry LogEntry
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return entry, false
	}
	for _, key := range [...]string{"time", "category", "level", "event", "module", "type", "content"} {
		if fields[key] == nil {
			return entry, false
		}
	}
	for key, value := range fields {
		if key == "content" {
			continue
		}
		var text string
		if string(value) == "null" || json.Unmarshal(value, &text) != nil {
			return entry, false
		}
		switch key {
		case "time":
			at, err := time.Parse(time.RFC3339Nano, text)
			if err != nil {
				return entry, false
			}
			entry.Time = at
		case "category":
			entry.Category = events.ParseLogCategory(text)
			if entry.Category == 0 {
				return entry, false
			}
		case "level":
			entry.Level = text
		case "event":
			entry.EventName = text
		case "module":
			entry.Module = text
		case "type":
			switch text {
			case "text":
				entry.Type = events.ContentText
			case "json":
				entry.Type = events.ContentJSON
			default:
				return entry, false
			}
		case "result_status":
			switch text {
			case "succeeded":
				entry.ResultStatus = events.ResultSucceeded
			case "failed":
				entry.ResultStatus = events.ResultFailed
			case "canceled":
				entry.ResultStatus = events.ResultCanceled
			case "rejected":
				entry.ResultStatus = events.ResultRejected
			case "skipped":
				entry.ResultStatus = events.ResultSkipped
			default:
				return entry, false
			}
		case "session_id":
			entry.Identity.SessionID = text
		case "run_id":
			entry.Identity.RunID = text
		case "attempt":
			entry.Identity.Attempt = text
		case "request_id":
			entry.Identity.RequestID = text
		case "root_request_id":
			entry.Identity.RootRequestID = text
		}
	}
	content := fields["content"]
	if entry.Type == events.ContentJSON {
		entry.Content = string(content)
	} else if string(content) == "null" || json.Unmarshal(content, &entry.Content) != nil {
		return entry, false
	}
	entry.Raw = raw
	return entry, true
}
