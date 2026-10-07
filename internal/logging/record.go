package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"unicode/utf8"

	"elbot/internal/events"
)

const (
	maxSummaryRunes = 256
	maxDetailBytes  = 8 * 1024
	maxRecordBytes  = 64 * 1024
	truncatedMarker = "...[truncated]"
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

func redactText(text string, depth int) string {
	text = strings.ToValidUTF8(text, "�")
	// Parse structured strings before shortening them; escaped JSON credentials
	// must not evade the same policy applied to actual nested values.
	trimmed := strings.TrimSpace(text)
	if depth < 32 && len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
		var value any
		decoder := json.NewDecoder(strings.NewReader(trimmed))
		decoder.UseNumber()
		if json.Valid([]byte(trimmed)) && decoder.Decode(&value) == nil {
			if data, err := json.Marshal(redactValue(value, depth+1)); err == nil {
				return string(data)
			}
		}
	}
	// Free-form text cannot reliably delimit a secret. Retain the explanation
	// preceding its first marker and discard the remaining text.
	if match := secretMarker.FindStringIndex(text); match != nil {
		return text[:match[1]] + "[redacted]"
	}
	return text
}

func redactValue(value any, depth int) any {
	if depth >= 32 {
		return "[depth limit]"
	}
	switch value := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(value))
		for key, item := range value {
			if secretKey(key) {
				result[key] = "[redacted]"
			} else {
				result[key] = redactValue(item, depth+1)
			}
		}
		return result
	case []any:
		result := make([]any, len(value))
		for i, item := range value {
			result[i] = redactValue(item, depth+1)
		}
		return result
	case string:
		return redactText(value, depth+1)
	default:
		return value
	}
}

func limitBytes(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	end := max(0, limit-len(truncatedMarker))
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end] + truncatedMarker
}

func limitRunes(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	return string(runes[:max(0, limit-utf8.RuneCountInString(truncatedMarker))]) + truncatedMarker
}

func safeAttr(attr slog.Attr) slog.Attr {
	if secretKey(attr.Key) {
		attr.Value = slog.StringValue("[redacted]")
	} else {
		switch attr.Value.Kind() {
		case slog.KindGroup:
			group := attr.Value.Group()
			attrs := make([]slog.Attr, len(group))
			for i, item := range group {
				attrs[i] = safeAttr(item)
			}
			attr.Value = slog.GroupValue(attrs...)
		case slog.KindString:
			attr.Value = slog.StringValue(limitBytes(redactText(attr.Value.String(), 0), maxDetailBytes))
		case slog.KindAny:
			data, err := json.Marshal(redactValue(attr.Value.Any(), 0))
			if err != nil {
				attr.Value = slog.StringValue("[unsupported log value]")
			} else {
				attr.Value = slog.StringValue(limitBytes(string(data), maxDetailBytes))
			}
		}
	}
	attr.Key = limitBytes(redactText(attr.Key, 0), 256)
	return attr
}

func identityKey(key string) bool {
	switch key {
	case "session_id", "run_id", "attempt", "request_id", "root_request_id":
		return true
	}
	return false
}

func formatRecord(ctx context.Context, record events.LogRecord, debug bool) ([]byte, error) {
	summary := limitRunes(redactText(record.Summary, 0), maxSummaryRunes)
	detail := ""
	if record.Category != events.LogRuntime || debug {
		detail = limitBytes(redactText(record.Detail, 0), maxDetailBytes)
	}
	core := []slog.Attr{slog.String("event", limitBytes(redactText(record.Name, 0), 256)), slog.String("module", limitBytes(redactText(record.Module, 0), 256))}
	var fields []slog.Attr
	seen := make(map[string]bool)
	var add func(slog.Attr)
	add = func(attr slog.Attr) {
		if attr.Key == "" && attr.Value.Kind() == slog.KindGroup {
			for _, item := range attr.Value.Group() {
				add(item)
			}
			return
		}
		switch attr.Key {
		case "time", "level", "msg", "event", "module", "detail", "truncated":
			return
		}
		if identityKey(attr.Key) {
			if !seen[attr.Key] {
				core = append(core, safeAttr(attr))
				seen[attr.Key] = true
			}
		} else {
			fields = append(fields, safeAttr(attr))
		}
	}
	for _, attr := range record.Fields {
		add(attr)
	}
	shortened := false
	for {
		var buffer bytes.Buffer
		handler := slog.NewTextHandler(&buffer, &slog.HandlerOptions{ReplaceAttr: replaceAttr})
		entry := slog.NewRecord(record.At, record.Level, summary, 0)
		entry.AddAttrs(core...)
		if detail != "" {
			entry.AddAttrs(slog.String("detail", detail))
		}
		entry.AddAttrs(fields...)
		if shortened {
			entry.AddAttrs(slog.Bool("truncated", true))
		}
		if err := handler.Handle(ctx, entry); err != nil {
			return nil, fmt.Errorf("format log record: %w", err)
		}
		if buffer.Len() <= maxRecordBytes {
			return buffer.Bytes(), nil
		}
		shortened = true
		switch {
		case detail != "":
			detail = ""
		case len(fields) > 0:
			fields = fields[:len(fields)/2]
		default:
			// Even pathological identity values must obey the serialized bound.
			for i, attr := range core {
				core[i] = slog.String(attr.Key, limitBytes(attr.Value.String(), 256))
			}
		}
	}
}
