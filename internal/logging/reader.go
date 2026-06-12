package logging

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultLogLimit = 5
	maxLogLineBytes = 16 * 1024 * 1024
)

type LogQuery struct {
	Prefix      string
	Limit       int
	Days        int
	MinLevel    string
	Since       *time.Time
	Until       *time.Time
	Fields      map[string]string
	FieldExists []string
	Contains    string
	MsgContains string
	Raw         bool
}

type LogEntry struct {
	Time    time.Time
	Level   string
	Message string
	Fields  map[string]string
	Raw     string
}

type Reader struct {
	Dir string
}

func (r Reader) Query(ctx context.Context, query LogQuery) ([]LogEntry, error) {
	if strings.TrimSpace(r.Dir) == "" {
		return nil, fmt.Errorf("log directory is not configured")
	}
	query = normalizeLogQuery(query)
	entries := []LogEntry{}
	for _, path := range logPaths(r.Dir, query.Prefix, query.Days) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		fileEntries, err := readLogFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for i := len(fileEntries) - 1; i >= 0; i-- {
			entry := fileEntries[i]
			if matchLogEntry(entry, query) {
				entries = append(entries, entry)
				if len(entries) >= query.Limit {
					return entries, nil
				}
			}
		}
	}
	return entries, nil
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

func readLogFile(path string) ([]LogEntry, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	entries := []LogEntry{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), maxLogLineBytes)
	for scanner.Scan() {
		raw := scanner.Text()
		entry := parseLogLine(raw)
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read log file %s: %w", path, err)
	}
	return entries, nil
}

func parseLogLine(raw string) LogEntry {
	fields := parseTextAttrs(raw)
	entry := LogEntry{Raw: raw, Fields: fields, Level: fields["level"], Message: fields["msg"]}
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
	if query.Since != nil && !entry.Time.IsZero() && entry.Time.Before(*query.Since) {
		return false
	}
	if query.Until != nil && !entry.Time.IsZero() && entry.Time.After(*query.Until) {
		return false
	}
	if query.MinLevel != "" && !levelAtLeast(entry.Level, query.MinLevel) {
		return false
	}
	for key, want := range query.Fields {
		if strings.TrimSpace(want) == "" {
			continue
		}
		if !strings.EqualFold(entry.Fields[key], want) {
			return false
		}
	}
	for _, key := range query.FieldExists {
		if strings.TrimSpace(entry.Fields[key]) == "" {
			return false
		}
	}
	if query.Contains != "" && !logEntryContains(entry, query.Contains) {
		return false
	}
	if query.MsgContains != "" && !containsFold(entry.Message, query.MsgContains) {
		return false
	}
	return true
}

func logEntryContains(entry LogEntry, needle string) bool {
	for _, key := range []string{"text", "raw_text", "arguments", "result", "latest_message_json", "first_system_message_json"} {
		if containsFold(entry.Fields[key], needle) {
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
		return 1
	}
}
