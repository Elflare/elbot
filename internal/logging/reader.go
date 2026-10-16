package logging

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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
	// Prefix selects the source category's daily files, without the date suffix.
	Prefix string
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
	// ContentFields matches JSON business values or historical attributes. Toolrun
	// text calls also expose their fixed leading run_source marker, not tool output.
	ContentFields map[string]string
	// ContentFieldContains requires all requested values to occur in a business array.
	ContentFieldContains map[string][]string
	// Contains searches decoded content, historical details and the raw record, case-insensitively.
	Contains string
	// ContentContains searches decoded content, case-insensitively.
	ContentContains string
	// Raw collects bounded damaged-line samples; it does not change valid entries.
	Raw bool
}

type LogEntry struct {
	// Time is center reception time for JSONL, or parsed historical source time.
	Time time.Time
	// Category is the JSONL category; historical records without it keep zero.
	Category events.LogCategory
	// Level is the declared severity; missing historical levels stay empty.
	Level string
	// EventName and Module preserve the source's public identifiers.
	EventName string
	Module    string
	// Type selects decoded text (zero) or JSON content; historical records are text.
	Type events.LogContentType
	// Content is decoded text or valid JSON with original integer precision.
	Content string
	// Identity preserves source presence; historical missing fields remain nil.
	Identity events.LogIdentity
	// ResultStatus is the declared overall result; zero means not declared.
	ResultStatus events.ResultStatus
	// Raw preserves the physical record, excluding its line terminator.
	Raw string
	// LegacyFields contains only historical text attributes; JSONL leaves it nil.
	LegacyFields map[string]string
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

type Reader struct {
	Dir string
}

func (r Reader) Query(ctx context.Context, query LogQuery) (LogQueryResult, error) {
	result := LogQueryResult{}
	if strings.TrimSpace(r.Dir) == "" {
		return result, fmt.Errorf("log directory is not configured")
	}
	query = normalizeLogQuery(query)
	length := 0
	for _, path := range logPaths(r.Dir, query.Prefix, query.Days) {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		err := readLogFileReverse(ctx, path, func(raw string) (bool, error) {
			if strings.TrimSpace(raw) == "" {
				return false, nil
			}
			entry := parseLogLine(raw)
			if strings.HasPrefix(strings.TrimSpace(raw), "{") || strings.HasPrefix(strings.TrimSpace(raw), "[") || json.Valid([]byte(raw)) {
				var fields map[string]json.RawMessage
				err := json.Unmarshal([]byte(raw), &fields)
				if err == nil {
					entry = LogEntry{Raw: raw}
					for key, value := range fields {
						var text string
						if key == "content" {
							continue
						}
						if err = json.Unmarshal(value, &text); err != nil || string(value) == "null" {
							err = fmt.Errorf("invalid log field %s", key)
							break
						}
						switch key {
						case "time":
							entry.Time, err = time.Parse(time.RFC3339Nano, text)
						case "level":
							entry.Level = text
						case "event":
							entry.EventName = text
						case "module":
							entry.Module = text
						case "category":
							switch text {
							case "runtime":
								entry.Category = events.LogRuntime
							case "audit":
								entry.Category = events.LogAudit
							case "elnis":
								entry.Category = events.LogElnis
							default:
								err = fmt.Errorf("invalid log category")
							}
						case "type":
							switch text {
							case "text":
								entry.Type = events.ContentText
							case "json":
								entry.Type = events.ContentJSON
							default:
								err = fmt.Errorf("invalid log type")
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
								err = fmt.Errorf("invalid log result")
							}
						case "session_id":
							entry.Identity.SessionID = &text
						case "run_id":
							entry.Identity.RunID = &text
						case "attempt":
							entry.Identity.Attempt = &text
						case "request_id":
							entry.Identity.RequestID = &text
						case "root_request_id":
							entry.Identity.RootRequestID = &text
						}
						if err != nil {
							break
						}
					}
					if err == nil {
						if fields["time"] == nil || fields["category"] == nil || fields["level"] == nil || fields["event"] == nil || fields["module"] == nil || fields["type"] == nil || fields["content"] == nil {
							err = fmt.Errorf("missing log field")
						} else if entry.Type == events.ContentText {
							err = json.Unmarshal(fields["content"], &entry.Content)
							if string(fields["content"]) == "null" {
								err = fmt.Errorf("null text content")
							}
						} else {
							entry.Content = string(fields["content"])
						}
					}
				}
				if err != nil {
					result.DamagedCount++
					if query.Raw && len(result.DamagedLines) < 10 && length+len(raw) <= 256*1024 {
						result.DamagedLines = append(result.DamagedLines, raw)
						length += len(raw)
					}
					return false, nil
				}
			}
			if matchLogEntry(entry, query) {
				result.Entries = append(result.Entries, entry)
			}
			return len(result.Entries) >= query.Limit, nil
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
	query.Prefix = strings.TrimSpace(query.Prefix)
	if query.Limit <= 0 {
		query.Limit = DefaultLogLimit
	}
	if query.Days <= 0 {
		query.Days = 1
	}
	query.MinLevel = strings.ToUpper(strings.TrimSpace(query.MinLevel))
	if query.Fields == nil {
		query.Fields = map[string]string{}
	}
	if query.ContentFieldContains == nil {
		query.ContentFieldContains = map[string][]string{}
	}
	return query
}

func logPaths(dir, prefix string, days int) []string {
	paths := make([]string, 0, days)
	today := time.Now()
	for i := 0; i < days; i++ {
		day := today.AddDate(0, 0, -i).Format("2006-01-02")
		paths = append(paths, filepath.Join(dir, prefix+"-"+day+".log"))
	}
	return paths
}

func readLogFileReverse(ctx context.Context, path string, visit func(string) (bool, error)) error {
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
// included in the 16 MiB limit, matching the text parser's LF/CRLF semantics.
func scanLogLinesReverse(ctx context.Context, reader io.ReaderAt, size int64, visit func(string) (bool, error)) error {
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
		for left, right := 0, len(line)-1; left < right; left, right = left+1, right-1 {
			line[left], line[right] = line[right], line[left]
		}
		raw := strings.TrimSuffix(string(line), "\r")
		stop, err := visit(raw)
		line = line[:0]
		if err != nil {
			return false, err
		}
		return stop, ctx.Err()
	}
	for offset := size; offset > 0; {
		if err := ctx.Err(); err != nil {
			return err
		}
		length := int64(len(block))
		if offset < length {
			length = offset
		}
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

func parseLogLine(raw string) LogEntry {
	fields := parseTextAttrs(raw)
	entry := LogEntry{Raw: raw, LegacyFields: fields, Level: fields["level"], Content: fields["msg"], EventName: fields["event"], Module: fields["module"]}
	for key, value := range fields {
		switch key {
		case "session_id":
			entry.Identity.SessionID = &value
		case "run_id":
			entry.Identity.RunID = &value
		case "attempt":
			entry.Identity.Attempt = &value
		case "request_id":
			entry.Identity.RequestID = &value
		case "root_request_id":
			entry.Identity.RootRequestID = &value
		case "result_status":
			switch value {
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
			}
		}
	}
	if ts, err := time.ParseInLocation("2006-01-02 15:04:05", fields["time"], time.Local); err == nil {
		entry.Time = ts
	}
	return entry
}

func parseTextAttrs(line string) map[string]string {
	out := map[string]string{}
	for i := 0; i < len(line); {
		for i < len(line) && line[i] == ' ' {
			i++
		}
		start := i
		for i < len(line) && line[i] != '=' && line[i] != ' ' {
			i++
		}
		if i >= len(line) || line[i] != '=' || start == i {
			for i < len(line) && line[i] != ' ' {
				i++
			}
			continue
		}
		key := line[start:i]
		i++
		value, next := parseTextValue(line, i)
		out[key] = value
		i = next
	}
	return out
}

func parseTextValue(line string, start int) (string, int) {
	if start >= len(line) {
		return "", start
	}
	if line[start] != '"' {
		i := start
		for i < len(line) && line[i] != ' ' {
			i++
		}
		return line[start:i], i
	}
	i := start + 1
	escaped := false
	for i < len(line) {
		c := line[i]
		if c == '\\' && !escaped {
			escaped = true
			i++
			continue
		}
		if c == '"' && !escaped {
			quoted := line[start : i+1]
			if unquoted, err := strconv.Unquote(quoted); err == nil {
				return unquoted, i + 1
			}
			return line[start+1 : i], i + 1
		}
		escaped = false
		i++
	}
	return line[start+1:], len(line)
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
	if query.MinLevel != "" && !levelAtLeast(entry.Level, query.MinLevel) {
		return false
	}
	fields := map[string]string{"event": entry.EventName, "module": entry.Module, "level": entry.Level, "category": entry.Category.String(), "type": entry.Type.String(), "result_status": entry.ResultStatus.String()}
	for key, value := range map[string]*string{"session_id": entry.Identity.SessionID, "run_id": entry.Identity.RunID, "attempt": entry.Identity.Attempt, "request_id": entry.Identity.RequestID, "root_request_id": entry.Identity.RootRequestID} {
		if value != nil {
			fields[key] = *value
		}
	}
	for key, want := range query.Fields {
		if strings.TrimSpace(want) == "" {
			continue
		}
		if !strings.EqualFold(fields[key], want) {
			return false
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
		value := entry.LegacyFields[key]
		// The marker belongs to toolrun's own header. Never search diagnostics
		// or parse arbitrary text bodies as business attributes.
		if key == "run_source" && entry.Type == events.ContentText && entry.Module == "toolrun" && entry.EventName == "tool_call" && strings.HasPrefix(entry.Content, "run_source=") {
			value, _, _ = strings.Cut(strings.TrimPrefix(entry.Content, "run_source="), " ")
			value, _, _ = strings.Cut(value, "\n")
		}
		if content != nil {
			value = string(content[key])
			if strings.HasPrefix(value, "\"") {
				_ = json.Unmarshal(content[key], &value)
			}
		}
		if !strings.EqualFold(value, want) {
			return false
		}
	}
	for key, needles := range query.ContentFieldContains {
		var values []string
		if content != nil {
			_ = json.Unmarshal(content[key], &values)
		} else {
			_ = json.Unmarshal([]byte(entry.LegacyFields[key]), &values)
		}
		for _, needle := range needles {
			if strings.TrimSpace(needle) == "" {
				continue
			}
			matched := false
			for _, value := range values {
				if strings.EqualFold(value, needle) {
					matched = true
					break
				}
			}
			if !matched {
				return false
			}
		}
	}
	if query.Contains != "" && !logEntryContains(entry, query.Contains) {
		return false
	}
	if query.ContentContains != "" && !containsFold(entry.Content, query.ContentContains) {
		return false
	}
	return true
}

func logEntryContains(entry LogEntry, needle string) bool {
	if containsFold(entry.Content, needle) {
		return true
	}
	for _, key := range []string{"detail", "text", "raw_text", "arguments", "result", "latest_message_json", "first_system_message_json"} {
		if containsFold(entry.LegacyFields[key], needle) {
			return true
		}
	}
	return containsFold(entry.Raw, needle)
}

func containsFold(value, needle string) bool {
	return strings.Contains(strings.ToLower(value), strings.ToLower(needle))
}

func levelAtLeast(got, min string) bool {
	return levelRank(got) >= levelRank(min)
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
