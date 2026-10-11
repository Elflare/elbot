package logging

import (
	"context"
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

func limitRunes(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	return string(runes[:limit-1]) + truncatedMarker
}

func formatRecord(ctx context.Context, record events.LogRecord, at time.Time) ([]byte, error) {
	var value any = record.Content
	switch record.Type {
	case events.ContentText:
		limit := record.ContentLimit
		if limit == 0 {
			limit = defaultContentLimit
		}
		value = limitRunes(record.Content, limit)
	case events.ContentJSON:
		// EmitLog already validates JSON. Preserve the source's field order and
		// values instead of decoding and rebuilding its body.
		value = json.RawMessage(record.Content)
	}
	fields := map[string]any{
		"time":     at.Format(time.RFC3339),
		"category": record.Category.String(),
		"level":    record.Level.String(),
		"event":    record.EventName,
		"module":   record.Module,
		"type":     record.Type.String(),
		"content":  value,
	}
	for key, value := range map[string]*string{
		"session_id":      record.Identity.SessionID,
		"run_id":          record.Identity.RunID,
		"attempt":         record.Identity.Attempt,
		"request_id":      record.Identity.RequestID,
		"root_request_id": record.Identity.RootRequestID,
	} {
		if value != nil {
			fields[key] = *value
		}
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
