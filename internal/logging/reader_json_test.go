package logging

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"elbot/internal/events"
)

func writeMixedLog(t *testing.T, lines ...string) Reader {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit-"+time.Now().Format("2006-01-02")+".log")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\r\n")), 0600); err != nil {
		t.Fatal(err)
	}
	return Reader{Dir: dir}
}

func jsonLogLine(t *testing.T, body string, identity *string) string {
	t.Helper()
	record := map[string]any{"time": "2026-10-08T12:34:56+08:00", "category": "audit", "level": "WARN", "event": "tool_call", "module": "tool", "type": "json", "content": json.RawMessage(body), "result_status": "failed"}
	if identity != nil {
		record["session_id"] = *identity
	}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestReaderJSONBusinessPredicatesBeforeLimit(t *testing.T) {
	empty := ""
	wanted := jsonLogLine(t, `{"tool":"shell","tags":["a","b"],"count":9007199254740993,"event":"forged","session_id":"forged"}`, &empty)
	r := writeMixedLog(t, wanted, jsonLogLine(t, `{"tool":"other","tags":["a","b"]}`, nil), `{broken`)
	query := LogQuery{Prefix: "audit", Limit: 1, Raw: true, Fields: map[string]string{"event": "tool_call"}, ContentFields: map[string]string{"tool": "shell", "count": "9007199254740993"}, ContentFieldContains: map[string][]string{"tags": {"a", "b"}}}
	result, err := r.Query(t.Context(), query)
	if err != nil || len(result.Entries) != 1 || result.DamagedCount != 1 || len(result.DamagedLines) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	entry := result.Entries[0]
	if entry.Raw != wanted || entry.LegacyFields != nil || entry.Identity.SessionID == nil || *entry.Identity.SessionID != "" || entry.ResultStatus != events.ResultFailed || entry.Category != events.LogAudit {
		t.Fatalf("entry=%+v", entry)
	}
	query.Fields["event"] = "forged"
	result, err = r.Query(t.Context(), query)
	if err != nil || len(result.Entries) != 0 {
		t.Fatalf("body overwrote public event: %+v %v", result, err)
	}
	query.Fields = map[string]string{"session_id": "forged"}
	result, err = r.Query(t.Context(), query)
	if err != nil || len(result.Entries) != 0 {
		t.Fatalf("body overwrote identity: %+v %v", result, err)
	}
}

func TestReaderMixedFormatsAndUnknownResults(t *testing.T) {
	r := writeMixedLog(t, `level=INFO msg="old body" event=tool_call tool=shell`, `{"time":"2026-10-08T00:00:00Z","category":"audit","level":"INFO","event":"tool_call","module":"tool","type":"text","content":"正文\n下一行"}`)
	result, err := r.Query(t.Context(), LogQuery{Prefix: "audit", Limit: 10})
	if err != nil || len(result.Entries) != 2 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if result.Entries[0].Content != "正文\n下一行" || result.Entries[0].ResultStatus != events.ResultUnspecified || result.Entries[0].Identity.SessionID != nil || result.Entries[1].LegacyFields["tool"] != "shell" {
		t.Fatalf("entries=%+v", result.Entries)
	}
	result, err = r.Query(t.Context(), LogQuery{Prefix: "audit", ContentFields: map[string]string{"tool": "shell"}})
	if err != nil || len(result.Entries) != 1 || result.Entries[0].Content != "old body" {
		t.Fatalf("legacy predicate: %+v %v", result, err)
	}
}

func TestReaderDamagedSamplesBoundedWithoutTruncation(t *testing.T) {
	lines := []string{jsonLogLine(t, `{}`, nil)}
	for i := 0; i < 12; i++ {
		lines = append(lines, `{broken`)
	}
	lines = append(lines, `{`+strings.Repeat("x", 256*1024))
	r := writeMixedLog(t, lines...)
	result, err := r.Query(t.Context(), LogQuery{Prefix: "audit", Limit: 1, Raw: true})
	if err != nil || len(result.Entries) != 1 || result.DamagedCount != 13 || len(result.DamagedLines) != 10 {
		t.Fatalf("result counts=%d/%d/%d err=%v", len(result.Entries), result.DamagedCount, len(result.DamagedLines), err)
	}
	for _, line := range result.DamagedLines {
		if line != "{broken" {
			t.Fatalf("truncated bad sample of %d bytes", len(line))
		}
	}
	result, err = r.Query(t.Context(), LogQuery{Prefix: "audit", Limit: 1})
	if err != nil || result.DamagedCount != 13 || len(result.DamagedLines) != 0 {
		t.Fatalf("normal result=%+v err=%v", result, err)
	}
}

func TestReaderMalformedJSONContractAndMissingLevel(t *testing.T) {
	r := writeMixedLog(t, `msg=unknown`, `null`, `[]`, `{}`, `{"time":4}`, `{broken`)
	result, err := r.Query(t.Context(), LogQuery{Prefix: "audit", Limit: 10, MinLevel: "INFO"})
	if err != nil || result.DamagedCount != 5 || len(result.Entries) != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
