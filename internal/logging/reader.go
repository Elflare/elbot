package logging

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"elbot/internal/events"
)

const (
	DefaultLogLimit   = 5
	maxLogLineBytes   = 16 * 1024 * 1024
	logReadBlockBytes = 64 * 1024
)

type LogQuery struct {
	// Limit caps matching valid entries; non-positive uses DefaultLogLimit.
	Limit int
	// Days scans today and preceding dates; non-positive means today only.
	Days int
	// MinLevel is an independent severity threshold; empty does not filter levels.
	MinLevel string
	// Since and Until bound recorded times; nil leaves that side unbounded.
	Since *time.Time
	Until *time.Time
	// Fields matches outer public fields; content cannot overwrite these values.
	Fields map[string]string
	// Events matches any listed event name exactly, case-insensitively; empty matches all.
	Events []string
	// ContentFields matches JSON business values; dots select nested object fields.
	ContentFields map[string]string
	// ContentFieldContains requires all requested values to occur in a business array.
	ContentFieldContains map[string][]string
	// Contains searches content and the raw record, case-insensitively.
	Contains string
	// ContentContains searches decoded content, case-insensitively.
	ContentContains string
	// Raw collects bounded damaged-line samples; it does not change valid entries.
	Raw bool
}

type LogEntry struct {
	// Time is the log center's reception time.
	Time time.Time
	// Category is the declared log category.
	Category events.LogCategory
	// Level is the declared severity.
	Level string
	// EventName and Module preserve the source's public identifiers.
	EventName string
	Module    string
	// Type selects decoded text (zero) or JSON content.
	Type events.LogContentType
	// Content is decoded text or valid JSON with original integer precision.
	Content string
	// Identity preserves source presence; missing fields remain empty.
	Identity events.LogIdentity
	// ResultStatus is the declared overall result; zero means not declared.
	ResultStatus events.ResultStatus
	// Raw preserves the physical record, excluding its line terminator.
	Raw string
}

type LogQueryResult struct {
	// Entries contains only valid records matching every requested predicate.
	Entries []LogEntry
	// DamagedCount counts malformed JSONL encountered in this scan, not the entire file.
	DamagedCount int
	// DamagedLines contains at most ten whole raw lines totaling at most 256 KiB.
	// It is collected only for Raw queries and does not imply a predicate match.
	DamagedLines []string
}

func (m *Manager) Query(ctx context.Context, category events.LogCategory, query LogQuery) (LogQueryResult, error) {
	var result LogQueryResult
	query = normalizeLogQuery(query)
	length := 0
	today := time.Now()
	for i := range query.Days {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		path := dailyLogPath(m.logDir, category.String(), today.AddDate(0, 0, -i).Format(time.DateOnly))
		err := readLogFileReverse(ctx, path, func(raw string) bool {
			if strings.TrimSpace(raw) == "" {
				return false
			}
			entry, ok := decodeRecord(raw)
			if !ok {
				result.DamagedCount++
				if query.Raw && len(result.DamagedLines) < 10 && length+len(raw) <= 256*1024 {
					result.DamagedLines = append(result.DamagedLines, raw)
					length += len(raw)
				}
				return false
			}
			if matchLogEntry(entry, query) {
				result.Entries = append(result.Entries, entry)
			}
			return len(result.Entries) >= query.Limit
		})
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return result, err
		}
		if len(result.Entries) >= query.Limit {
			return result, nil
		}
	}
	return result, nil
}

func normalizeLogQuery(query LogQuery) LogQuery {
	if query.Limit <= 0 {
		query.Limit = DefaultLogLimit
	}
	if query.Days <= 0 {
		query.Days = 1
	}
	return query
}

func readLogFileReverse(ctx context.Context, path string, visit func(string) bool) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat log file %s: %w", path, err)
	}
	if err := scanLogLinesReverse(ctx, file, info.Size(), visit); err != nil {
		return fmt.Errorf("read log file %s: %w", path, err)
	}
	return nil
}

// scanLogLinesReverse visits physical lines newest first within a fixed range.
// Returning true from visit stops reading immediately. Line terminators are not
// included in the 16 MiB limit, for both LF and CRLF records.
func scanLogLinesReverse(ctx context.Context, reader io.ReaderAt, size int64, visit func(string) bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if size == 0 {
		return nil
	}
	block := make([]byte, logReadBlockBytes)
	// Append bytes in reverse order to avoid repeatedly prepending and copying
	// a long line at every block boundary. Reverse only when the line is complete.
	line := make([]byte, 0, logReadBlockBytes)
	atEnd := true
	emit := func() (bool, error) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		slices.Reverse(line)
		raw := strings.TrimSuffix(string(line), "\r")
		stop := visit(raw)
		line = line[:0]
		return stop, ctx.Err()
	}
	for offset := size; offset > 0; {
		if err := ctx.Err(); err != nil {
			return err
		}
		length := min(int64(len(block)), offset)
		offset -= length
		n, err := reader.ReadAt(block[:length], offset)
		if err != nil && (err != io.EOF || int64(n) != length) {
			return err
		}
		if int64(n) != length {
			return io.ErrUnexpectedEOF
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		for i := n - 1; i >= 0; i-- {
			if block[i] == '\n' {
				if !atEnd {
					stop, err := emit()
					if err != nil || stop {
						return err
					}
				}
			} else {
				line = append(line, block[i])
				contentBytes := len(line)
				if line[0] == '\r' {
					contentBytes--
				}
				if contentBytes > maxLogLineBytes {
					return fmt.Errorf("log line exceeds %d bytes", maxLogLineBytes)
				}
			}
			atEnd = false
		}
	}
	_, err := emit()
	return err
}

func matchLogEntry(entry LogEntry, query LogQuery) bool {
	if len(query.Events) != 0 && !slices.ContainsFunc(query.Events, func(event string) bool {
		return strings.EqualFold(entry.EventName, event)
	}) {
		return false
	}
	if query.Since != nil && !entry.Time.IsZero() && entry.Time.Before(*query.Since) {
		return false
	}
	if query.Until != nil && !entry.Time.IsZero() && entry.Time.After(*query.Until) {
		return false
	}
	if query.MinLevel != "" && levelRank(entry.Level) < levelRank(query.MinLevel) {
		return false
	}
	if len(query.Fields) != 0 {
		fields := map[string]string{"event": entry.EventName, "module": entry.Module, "level": entry.Level, "category": entry.Category.String(), "type": entry.Type.String(), "result_status": entry.ResultStatus.String()}
		fields["session_id"] = entry.Identity.SessionID
		fields["run_id"] = entry.Identity.RunID
		fields["attempt"] = entry.Identity.Attempt
		fields["request_id"] = entry.Identity.RequestID
		fields["root_request_id"] = entry.Identity.RootRequestID
		for key, want := range query.Fields {
			if strings.TrimSpace(want) == "" {
				continue
			}
			if !strings.EqualFold(fields[key], want) {
				return false
			}
		}
	}
	var content map[string]json.RawMessage
	if entry.Type == events.ContentJSON && (len(query.ContentFields) != 0 || len(query.ContentFieldContains) != 0) {
		_ = json.Unmarshal([]byte(entry.Content), &content)
	}
	for key, want := range query.ContentFields {
		if strings.TrimSpace(want) == "" {
			continue
		}
		parts := strings.Split(key, ".")
		raw := content[parts[0]]
		for _, part := range parts[1:] {
			var object map[string]json.RawMessage
			_ = json.Unmarshal(raw, &object)
			raw = object[part]
		}
		value := string(raw)
		if strings.HasPrefix(value, "\"") {
			_ = json.Unmarshal(raw, &value)
		}
		if value != want {
			return false
		}
	}
	for key, needles := range query.ContentFieldContains {
		var values []string
		_ = json.Unmarshal(content[key], &values)
		for _, needle := range needles {
			if strings.TrimSpace(needle) == "" {
				continue
			}
			if !slices.ContainsFunc(values, func(value string) bool {
				return strings.EqualFold(value, needle)
			}) {
				return false
			}
		}
	}
	if query.Contains != "" && !containsFold(entry.Content, query.Contains) && !containsFold(entry.Raw, query.Contains) {
		return false
	}
	if query.ContentContains != "" && !containsFold(entry.Content, query.ContentContains) {
		return false
	}
	return true
}

func containsFold(value, needle string) bool {
	return strings.Contains(strings.ToLower(value), strings.ToLower(needle))
}

func levelRank(level string) int {
	switch strings.ToUpper(strings.TrimSpace(level)) {
	case "DEBUG":
		return 0
	case "INFO":
		return 1
	case "WARN", "WARNING":
		return 2
	case "ERROR":
		return 3
	default:
		return -1
	}
}
