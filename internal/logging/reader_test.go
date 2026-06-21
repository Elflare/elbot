package logging

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
