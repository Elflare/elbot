package logging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestReaderStopsBeforeOlderOversizedLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "elbot-"+time.Now().Format("2006-01-02")+".log")
	content := strings.Repeat("x", maxLogLineBytes+1) + "\nlevel=INFO msg=latest\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := (Reader{Dir: dir}).Query(context.Background(), LogQuery{Prefix: "elbot", Limit: 1})
	if err != nil || len(entries) != 1 || entries[0].Message != "latest" {
		t.Fatalf("tail query: entries=%v err=%v", entries, err)
	}
}

func TestReaderParsesAndFiltersTextLogs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit-"+time.Now().Format("2006-01-02")+".log")
	content := "time=\"2026-06-03 15:00:00\" level=INFO msg=\"audit event\" event=tool_call risk=high tool=shell error=\"bad thing\"\n" +
		"time=\"2026-06-03 15:01:00\" level=INFO msg=\"audit event\" event=llm_usage model=foo total_tokens=42\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	entries, err := (Reader{Dir: dir}).Query(context.Background(), LogQuery{
		Prefix: "audit",
		Fields: map[string]string{"event": "tool_call", "risk": "high"},
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %#v", entries)
	}
	entry := entries[0]
	if entry.Message != "audit event" || entry.Fields["tool"] != "shell" || entry.Fields["error"] != "bad thing" {
		t.Fatalf("entry = %#v", entry)
	}
}

func TestReaderContainsMatchesStructuredFieldsAndRaw(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "elbot-"+time.Now().Format("2006-01-02")+".log")
	content := "time=\"2026-06-03 15:00:00\" level=INFO msg=\"user input\" event=user_message text=\"hello world\"\n" +
		"time=\"2026-06-03 15:01:00\" level=INFO msg=\"tool call\" event=tool_call arguments=\"{\\\"path\\\":\\\"a.txt\\\"}\" result=\"file content\"\n" +
		"time=\"2026-06-03 15:02:00\" level=INFO msg=other custom_field=needle\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	entries, err := (Reader{Dir: dir}).Query(context.Background(), LogQuery{Prefix: "elbot", Contains: "file content"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(entries) != 1 || entries[0].Fields["event"] != "tool_call" {
		t.Fatalf("entries = %#v", entries)
	}

	entries, err = (Reader{Dir: dir}).Query(context.Background(), LogQuery{Prefix: "elbot", Contains: "needle"})
	if err != nil {
		t.Fatalf("Query raw: %v", err)
	}
	if len(entries) != 1 || entries[0].Message != "other" {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestReaderFieldContainsMatchesOnlyRequestedField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "elnis-"+time.Now().Format("2006-01-02")+".log")
	content := "time=\"2026-06-03 15:00:00\" level=INFO msg=\"elnis event accepted\" tags=\"[\\\"windows\\\",\\\"onedrive\\\"]\" raw_text=watchdog\n" +
		"time=\"2026-06-03 15:01:00\" level=INFO msg=\"elnis event accepted\" tags=\"[\\\"watchdog\\\"]\" raw_text=onedrive\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	entries, err := (Reader{Dir: dir}).Query(context.Background(), LogQuery{Prefix: "elnis", FieldContains: map[string][]string{"tags": {"\"onedrive\""}}})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(entries) != 1 || entries[0].Fields["tags"] != "[\"windows\",\"onedrive\"]" {
		t.Fatalf("entries = %#v", entries)
	}

	entries, err = (Reader{Dir: dir}).Query(context.Background(), LogQuery{Prefix: "elnis", FieldContains: map[string][]string{"tags": {"\"windows\"", "\"onedrive\""}}})
	if err != nil {
		t.Fatalf("Query both tags: %v", err)
	}
	if len(entries) != 1 || entries[0].Fields["tags"] != "[\"windows\",\"onedrive\"]" {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestReaderAppliesMinLevelAndLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "elbot-"+time.Now().Format("2006-01-02")+".log")
	content := "time=\"2026-06-03 15:00:00\" level=DEBUG msg=debug\n" +
		"time=\"2026-06-03 15:01:00\" level=INFO msg=info\n" +
		"time=\"2026-06-03 15:02:00\" level=ERROR msg=error\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	entries, err := (Reader{Dir: dir}).Query(context.Background(), LogQuery{Prefix: "elbot", MinLevel: "info", Limit: 1})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(entries) != 1 || entries[0].Message != "error" {
		t.Fatalf("entries = %#v", entries)
	}
}

type countedLogReader struct {
	io.ReaderAt
	bytes     int
	reads     int
	afterRead func()
}

func (r *countedLogReader) ReadAt(p []byte, offset int64) (int, error) {
	n, err := r.ReaderAt.ReadAt(p, offset)
	r.bytes += n
	r.reads++
	if r.afterRead != nil {
		r.afterRead()
	}
	return n, err
}

func TestReverseLogScanStopsAfterOneBlock(t *testing.T) {
	content := strings.Repeat("level=INFO msg=old\n", 100000) + "level=INFO msg=latest\n"
	reader := &countedLogReader{ReaderAt: strings.NewReader(content)}
	var lines []string
	err := scanLogLinesReverse(context.Background(), reader, int64(len(content)), func(line string) (bool, error) {
		lines = append(lines, line)
		return true, nil
	})
	if err != nil || !slices.Equal(lines, []string{"level=INFO msg=latest"}) {
		t.Fatalf("lines=%v err=%v", lines, err)
	}
	if reader.reads != 1 || reader.bytes != logReadBlockBytes {
		t.Fatalf("read %d bytes in %d calls for one tail record", reader.bytes, reader.reads)
	}
}

func TestReverseLogScanLineBoundaries(t *testing.T) {
	long := strings.Repeat("中文🙂", logReadBlockBytes/3)
	for _, tc := range []struct {
		name    string
		content string
		want    []string
	}{
		{"empty", "", nil},
		{"one_empty_line", "\n", []string{""}},
		{"blank_lines", "\nfirst\n\nlast\n", []string{"last", "", "first", ""}},
		{"unterminated", "first\nlast", []string{"last", "first"}},
		{"crlf", "first\r\nlast\r\n", []string{"last", "first"}},
		{"cross_block_unicode", "first\n" + long + "\nlast", []string{"last", long, "first"}},
		{"newline_on_boundary", "first\n" + strings.Repeat("x", logReadBlockBytes-1) + "\n", []string{strings.Repeat("x", logReadBlockBytes-1), "first"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			err := scanLogLinesReverse(context.Background(), strings.NewReader(tc.content), int64(len(tc.content)), func(line string) (bool, error) {
				got = append(got, line)
				return false, nil
			})
			if err != nil || !slices.Equal(got, tc.want) {
				t.Fatalf("line mismatch: got lengths=%v want lengths=%v err=%v", logLineLengths(got), logLineLengths(tc.want), err)
			}
		})
	}
}

func logLineLengths(lines []string) []int {
	lengths := make([]int, len(lines))
	for i, line := range lines {
		lengths[i] = len(line)
	}
	return lengths
}

func TestReverseLogScanLineSizeLimit(t *testing.T) {
	for _, suffix := range []string{"", "\n", "\r\n"} {
		for _, size := range []int{maxLogLineBytes, maxLogLineBytes + 1} {
			t.Run(fmt.Sprintf("%d/%q", size, suffix), func(t *testing.T) {
				content := strings.Repeat("x", size) + suffix
				seen := 0
				err := scanLogLinesReverse(context.Background(), strings.NewReader(content), int64(len(content)), func(line string) (bool, error) {
					seen++
					if len(line) != size {
						t.Fatalf("line length=%d", len(line))
					}
					return false, nil
				})
				if size == maxLogLineBytes {
					if err != nil || seen != 1 {
						t.Fatalf("boundary: seen=%d err=%v", seen, err)
					}
				} else if err == nil || !strings.Contains(err.Error(), "log line exceeds") || seen != 0 {
					t.Fatalf("oversize: seen=%d err=%v", seen, err)
				}
			})
		}
	}
}

func TestReverseLogScanCancellation(t *testing.T) {
	for _, when := range []string{"before", "read", "line", "long_line"} {
		t.Run(when, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			content := "first\nlast\n"
			if when == "long_line" {
				content = strings.Repeat("x", logReadBlockBytes*3)
			}
			reader := &countedLogReader{ReaderAt: strings.NewReader(content)}
			if when == "before" {
				cancel()
			}
			if when == "read" || when == "long_line" {
				reader.afterRead = cancel
			}
			seen := 0
			err := scanLogLinesReverse(ctx, reader, int64(len(content)), func(string) (bool, error) {
				seen++
				cancel()
				return false, nil
			})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("err=%v", err)
			}
			wantReads, wantSeen := 1, 0
			if when == "before" {
				wantReads = 0
			}
			if when == "line" {
				wantSeen = 1
			}
			if reader.reads != wantReads || seen != wantSeen {
				t.Fatalf("reads=%d seen=%d", reader.reads, seen)
			}
		})
	}
}

func TestReverseLogScanFixedRangeAndErrors(t *testing.T) {
	content := "first\nsecond\n"
	var got []string
	err := scanLogLinesReverse(context.Background(), strings.NewReader(content+"appended\n"), int64(len(content)), func(line string) (bool, error) {
		got = append(got, line)
		return false, nil
	})
	if err != nil || !slices.Equal(got, []string{"second", "first"}) {
		t.Fatalf("got=%v err=%v", got, err)
	}
	err = scanLogLinesReverse(context.Background(), strings.NewReader(content), int64(len(content)+1), func(string) (bool, error) {
		t.Fatal("visited data after short read")
		return false, nil
	})
	if !errors.Is(err, io.EOF) {
		t.Fatalf("short read: %v", err)
	}
	wantErr := errors.New("visitor failed")
	err = scanLogLinesReverse(context.Background(), strings.NewReader(content), int64(len(content)), func(string) (bool, error) { return false, wantErr })
	if !errors.Is(err, wantErr) {
		t.Fatalf("visitor error: %v", err)
	}
}

type logReadAtFunc func([]byte, int64) (int, error)

func (f logReadAtFunc) ReadAt(p []byte, offset int64) (int, error) { return f(p, offset) }

func TestReverseLogScanReadAtResults(t *testing.T) {
	failure := errors.New("read failed")
	for _, tc := range []struct {
		name         string
		n            int
		err, wantErr error
	}{
		{"full_with_eof", 3, io.EOF, nil},
		{"short_without_error", 2, nil, io.ErrUnexpectedEOF},
		{"read_failure", 0, failure, failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := logReadAtFunc(func(p []byte, _ int64) (int, error) { copy(p, "ok\n"); return tc.n, tc.err })
			var lines []string
			err := scanLogLinesReverse(context.Background(), reader, 3, func(line string) (bool, error) { lines = append(lines, line); return false, nil })
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err=%v want=%v", err, tc.wantErr)
			}
			if tc.wantErr == nil && !slices.Equal(lines, []string{"ok"}) {
				t.Fatalf("lines=%v", lines)
			}
			if tc.wantErr != nil && len(lines) != 0 {
				t.Fatalf("visited failed read: %v", lines)
			}
		})
	}
}

func TestReaderCrossDayFiltersAndPhysicalOrder(t *testing.T) {
	dir := t.TempDir()
	paths := logPaths(dir, "audit", 3)
	// Event timestamps need not be monotonic within a file. Keep write order.
	line := func(hour, module, detail string) string {
		return fmt.Sprintf("time=%q level=WARN msg=%q event=hook_tool_call module=%s detail=%q tags=%q\n", "2026-06-03 "+hour+":00:00", "摘要\n匹配", module, detail, `["tag"]`)
	}
	if err := os.WriteFile(paths[0], []byte(line("12", "hook", "详情\n目标")+line("11", "hook", "详情\n目标")+line("13", "agent", "详情\n目标")), 0o600); err != nil {
		t.Fatal(err)
	}
	// A missing day is skipped.
	if err := os.WriteFile(paths[2], []byte(line("10", "hook", "详情\n目标")), 0o600); err != nil {
		t.Fatal(err)
	}
	since, _ := time.ParseInLocation("2006-01-02 15:04:05", "2026-06-03 10:00:00", time.Local)
	until := since.Add(2 * time.Hour)
	entries, err := (Reader{Dir: dir}).Query(context.Background(), LogQuery{
		Prefix: "audit", Days: 3, Limit: 3, MinLevel: "warn", Since: &since, Until: &until,
		Fields:      map[string]string{"module": "hook", "event": "hook_tool_call"},
		FieldExists: []string{"detail"}, FieldContains: map[string][]string{"tags": {"tag"}},
		Contains: "详情\n目标", MsgContains: "摘要\n匹配",
	})
	if err != nil || len(entries) != 3 {
		t.Fatalf("entries=%v err=%v", entries, err)
	}
	for i, hour := range []int{11, 12, 10} {
		if entries[i].Time.Hour() != hour {
			t.Fatalf("order: %v", entries)
		}
	}
	entries, err = (Reader{Dir: dir}).Query(context.Background(), LogQuery{Prefix: "audit", Contains: "摘要\n匹配"})
	if err != nil || len(entries) != 3 {
		t.Fatalf("decoded summary: count=%d err=%v", len(entries), err)
	}
}

func TestReaderLimitSkipsOlderFilesAndPropagatesReadErrors(t *testing.T) {
	dir := t.TempDir()
	paths := logPaths(dir, "elbot", 2)
	if err := os.WriteFile(paths[0], []byte("level=INFO msg=latest\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(paths[1], 0o700); err != nil {
		t.Fatal(err)
	}
	reader := Reader{Dir: dir}
	entries, err := reader.Query(context.Background(), LogQuery{Prefix: "elbot", Days: 2, Limit: 1})
	if err != nil || len(entries) != 1 {
		t.Fatalf("limit: entries=%v err=%v", entries, err)
	}
	_, err = reader.Query(context.Background(), LogQuery{Prefix: "elbot", Days: 2, Limit: 2})
	if err == nil || !strings.Contains(err.Error(), paths[1]) {
		t.Fatalf("file error: %v", err)
	}
}

func TestReaderContainsDecodedDetailAndRaw(t *testing.T) {
	entry := parseLogLine("msg=" + strconv.Quote("摘要\n下一行") + " detail=" + strconv.Quote("详情\n下一行") + " custom=value")
	for _, needle := range []string{"摘要\n下一行", "详情\n下一行", "custom=value"} {
		if !matchLogEntry(entry, LogQuery{Contains: needle}) {
			t.Fatalf("not matched: %q", needle)
		}
	}
}
