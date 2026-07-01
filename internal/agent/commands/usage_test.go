package commands

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"elbot/internal/command"
	"elbot/internal/logging"
)

func makeUsageEntry(t time.Time, provider, model, sessionID string, prompt, completion, total, cache int, elapsedMs int64) logging.LogEntry {
	return logging.LogEntry{
		Time:    t,
		Level:   "INFO",
		Message: "audit event",
		Fields: map[string]string{
			"event":             "llm_usage",
			"session_id":        sessionID,
			"provider":          provider,
			"model":             model,
			"prompt_tokens":     strconv.Itoa(prompt),
			"completion_tokens": strconv.Itoa(completion),
			"total_tokens":      strconv.Itoa(total),
			"cache_hit_tokens":  strconv.Itoa(cache),
			"elapsed_ms":        strconv.FormatInt(elapsedMs, 10),
		},
	}
}

func TestUsageDefaultQueryByModel(t *testing.T) {
	entries := []logging.LogEntry{
		makeUsageEntry(time.Date(2026, 7, 1, 10, 0, 0, 0, time.Local), "deepseek", "deepseek-v4", "sess-1", 1000, 200, 1200, 500, 1500),
		makeUsageEntry(time.Date(2026, 7, 1, 11, 0, 0, 0, time.Local), "deepseek", "deepseek-v4", "sess-1", 800, 100, 900, 0, 800),
		makeUsageEntry(time.Date(2026, 7, 1, 12, 0, 0, 0, time.Local), "openai", "gpt-4o", "sess-2", 500, 50, 550, 0, 300),
	}
	service := &fakeLogService{entries: entries}
	result, err := NewUsage(Deps{Logs: service}).Handle(context.Background(), command.Request{Args: ""})
	if err != nil {
		t.Fatalf("usage handle: %v", err)
	}
	if service.query.Prefix != "audit" || service.query.Fields["event"] != "llm_usage" {
		t.Fatalf("query = %#v", service.query)
	}
	for _, want := range []string{
		"by model",
		"[deepseek/deepseek-v4] calls: 2",
		"prompt: 1,800 | completion: 300 | total: 2,100",
		"cache: 500 | elapsed: 2.3s",
		"[openai/gpt-4o] calls: 1",
		"prompt: 500 | completion: 50 | total: 550",
		"cache: 0 | elapsed: 0.3s",
		"[total] calls: 3",
		"prompt: 2,300 | completion: 350 | total: 2,650",
		"cache: 500 | elapsed: 2.6s",
	} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("content missing %q:\n%s", want, result.Content)
		}
	}
}

func TestUsageQueryByDay(t *testing.T) {
	entries := []logging.LogEntry{
		makeUsageEntry(time.Date(2026, 6, 30, 10, 0, 0, 0, time.Local), "deepseek", "deepseek-v4", "sess-1", 1000, 200, 1200, 0, 1000),
		makeUsageEntry(time.Date(2026, 7, 1, 10, 0, 0, 0, time.Local), "deepseek", "deepseek-v4", "sess-2", 500, 100, 600, 0, 500),
		makeUsageEntry(time.Date(2026, 7, 1, 12, 0, 0, 0, time.Local), "openai", "gpt-4o", "sess-3", 300, 50, 350, 0, 300),
	}
	service := &fakeLogService{entries: entries}
	result, err := NewUsage(Deps{Logs: service}).Handle(context.Background(), command.Request{Args: "--by day -d 7"})
	if err != nil {
		t.Fatalf("usage handle: %v", err)
	}
	if service.query.Days != 7 {
		t.Fatalf("Days = %d", service.query.Days)
	}
	for _, want := range []string{
		"by day",
		"[2026-06-30] calls: 1",
		"prompt: 1,000 | completion: 200 | total: 1,200",
		"cache: 0 | elapsed: 1.0s",
		"[2026-07-01] calls: 2",
		"prompt: 800 | completion: 150 | total: 950",
		"cache: 0 | elapsed: 0.8s",
		"[total] calls: 3",
		"prompt: 1,800 | completion: 350 | total: 2,150",
		"cache: 0 | elapsed: 1.8s",
	} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("content missing %q:\n%s", want, result.Content)
		}
	}
}

func TestUsageQueryBySession(t *testing.T) {
	entries := []logging.LogEntry{
		makeUsageEntry(time.Date(2026, 7, 1, 10, 0, 0, 0, time.Local), "deepseek", "deepseek-v4", "sess-1", 1000, 200, 1200, 100, 1000),
		makeUsageEntry(time.Date(2026, 7, 1, 11, 0, 0, 0, time.Local), "deepseek", "deepseek-v4", "sess-1", 500, 100, 600, 0, 500),
		makeUsageEntry(time.Date(2026, 7, 1, 12, 0, 0, 0, time.Local), "openai", "gpt-4o", "sess-2", 300, 50, 350, 0, 300),
	}
	service := &fakeLogService{entries: entries}
	result, err := NewUsage(Deps{Logs: service}).Handle(context.Background(), command.Request{Args: "--by session"})
	if err != nil {
		t.Fatalf("usage handle: %v", err)
	}
	for _, want := range []string{
		"by session",
		"[sess-1] calls: 2",
		"prompt: 1,500 | completion: 300 | total: 1,800",
		"cache: 100 | elapsed: 1.5s",
		"[sess-2] calls: 1",
		"prompt: 300 | completion: 50 | total: 350",
		"cache: 0 | elapsed: 0.3s",
		"[total] calls: 3",
		"prompt: 1,800 | completion: 350 | total: 2,150",
		"cache: 100 | elapsed: 1.8s",
	} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("content missing %q:\n%s", want, result.Content)
		}
	}
}

func TestUsageFiltersByModel(t *testing.T) {
	service := &fakeLogService{entries: nil}
	_, err := NewUsage(Deps{Logs: service}).Handle(context.Background(), command.Request{Args: "-m gpt-4o"})
	if err != nil {
		t.Fatalf("usage handle: %v", err)
	}
	if service.query.Fields["model"] != "gpt-4o" {
		t.Fatalf("model filter = %q", service.query.Fields["model"])
	}
}

func TestUsageFiltersBySession(t *testing.T) {
	service := &fakeLogService{entries: nil}
	_, err := NewUsage(Deps{Logs: service}).Handle(context.Background(), command.Request{Args: "-s sess-1"})
	if err != nil {
		t.Fatalf("usage handle: %v", err)
	}
	if service.query.Fields["session_id"] != "sess-1" {
		t.Fatalf("session filter = %q", service.query.Fields["session_id"])
	}
}

func TestUsageNoData(t *testing.T) {
	service := &fakeLogService{entries: nil}
	result, err := NewUsage(Deps{Logs: service}).Handle(context.Background(), command.Request{Args: ""})
	if err != nil {
		t.Fatalf("usage handle: %v", err)
	}
	if result.Content != "no usage data found" {
		t.Fatalf("content = %q", result.Content)
	}
}

func TestUsageSinceFilter(t *testing.T) {
	entries := []logging.LogEntry{
		makeUsageEntry(time.Date(2026, 7, 1, 10, 0, 0, 0, time.Local), "deepseek", "deepseek-v4", "sess-1", 1000, 200, 1200, 0, 1000),
	}
	service := &fakeLogService{entries: entries}
	_, err := NewUsage(Deps{Logs: service}).Handle(context.Background(), command.Request{Args: "--since 2h"})
	if err != nil {
		t.Fatalf("usage handle: %v", err)
	}
	if service.query.Since == nil {
		t.Fatalf("Since should be set")
	}
}

func TestUsageInvalidByOption(t *testing.T) {
	service := &fakeLogService{entries: nil}
	_, err := NewUsage(Deps{Logs: service}).Handle(context.Background(), command.Request{Args: "--by foo"})
	if err == nil || !strings.Contains(err.Error(), "must be model, day or session") {
		t.Fatalf("expected error, got: %v", err)
	}
}

func TestUsageShortFlags(t *testing.T) {
	service := &fakeLogService{entries: nil}
	_, err := NewUsage(Deps{Logs: service}).Handle(context.Background(), command.Request{Args: "-d 7 -m gpt-4o -s sess-1"})
	if err != nil {
		t.Fatalf("usage handle: %v", err)
	}
	if service.query.Days != 7 || service.query.Fields["model"] != "gpt-4o" || service.query.Fields["session_id"] != "sess-1" {
		t.Fatalf("query = %#v", service.query)
	}
}

func TestUsageCompletionByOption(t *testing.T) {
	c := usageCommand{}
	completions := c.Complete(context.Background(), command.CompletionRequest{
		Raw: "/usage --by mo", Prefix: "/", Name: "usage", Args: "--by mo", Cursor: 15,
	})
	if len(completions) == 0 || completions[0].Text != "model" {
		t.Fatalf("expected model completion, got: %#v", completions)
	}
}

func TestUsageCompletionOptions(t *testing.T) {
	c := usageCommand{}
	completions := c.Complete(context.Background(), command.CompletionRequest{
		Raw: "/usage -", Prefix: "/", Name: "usage", Args: "-", Cursor: 8,
	})
	texts := make([]string, len(completions))
	for i, comp := range completions {
		texts[i] = comp.Text
	}
	for _, want := range []string{"-d", "--days", "-m", "--model", "-s", "--session", "--by", "--since", "--until"} {
		found := false
		for _, text := range texts {
			if text == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing completion option %q in %v", want, texts)
		}
	}
}
