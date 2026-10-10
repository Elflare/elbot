package logging

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"elbot/internal/events"
)

const (
	defaultContentLimit = 256
	maxRecordBytes      = 64 * 1024
	truncatedMarker     = "…"
)

var secretMarker = regexp.MustCompile(`(?i)(?:\b(?:authorization|proxy[-_]?authorization|(?:x[-_])?api[-_]?key|access[-_]?token|refresh[-_]?token|id[-_]?token|token|password|client[-_]?secret|secret|cookie|set[-_]?cookie)\b["']?\s*[:=]\s*|\bbearer\s+|;base64,|base64://)`)

func secretKey(key string) bool {
	key = strings.ToLower(strings.NewReplacer("_", "", "-", "", ".", "").Replace(key))
	switch key {
	case "authorization", "proxyauthorization", "apikey", "xapikey", "token", "accesstoken", "refreshtoken", "idtoken", "password", "secret", "clientsecret", "cookie", "setcookie", "encryptedcontent", "imagedata", "filedata", "audiodata", "base64":
		return true
	}
	return false
}

func redactText(text string) string {
	text = strings.ToValidUTF8(text, "�")
	// Free-form text cannot reliably delimit a secret. Keep the explanation
	// before its first marker, without guessing whether the text is JSON.
	if match := secretMarker.FindStringIndex(text); match != nil {
		return text[:match[1]] + "[redacted]"
	}
	return text
}

// redactValue only visits values decoded from JSON with UseNumber.
// It does not convert arbitrary business objects or discard deep subtrees.
func redactValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		for key, item := range value {
			if secretKey(key) {
				value[key] = "[redacted]"
			} else {
				value[key] = redactValue(item)
			}
		}
	case []any:
		for i, item := range value {
			value[i] = redactValue(item)
		}
	case string:
		return redactText(value)
	}
	return value
}

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
		value = limitRunes(redactText(record.Content), limit)
	case events.ContentJSON:
		// EmitLog validates the contract once. Decode here only to redact JSON
		// values while preserving numbers, not to validate the source again.
		decoder := json.NewDecoder(strings.NewReader(record.Content))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("format log record: %w", err)
		}
		value = redactValue(value)
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
