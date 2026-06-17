package commands

import (
	"context"
	"strings"
	"testing"
	"time"

	"elbot/internal/command"
	"elbot/internal/logging"
	"elbot/internal/tool"
)

type fakeLogService struct {
	query   logging.LogQuery
	entries []logging.LogEntry
}

func (s *fakeLogService) QueryLogs(ctx context.Context, query logging.LogQuery) ([]logging.LogEntry, error) {
	s.query = query
	return s.entries, nil
}

func TestAuditCommandParsesFilters(t *testing.T) {
	service := &fakeLogService{entries: []logging.LogEntry{{
		Time:   time.Date(2026, 6, 3, 15, 0, 0, 0, time.Local),
		Fields: map[string]string{"event": "tool_call", "risk": "high", "tool": "shell"},
	}}}
	result, err := NewAudit(Deps{Logs: service}).Handle(context.Background(), command.Request{Args: "--event tool_call --risk high --tool shell -n 3 --days 2"})
	if err != nil {
		t.Fatalf("audit handle: %v", err)
	}
	if service.query.Prefix != "audit" || service.query.Limit != 3 || service.query.Days != 2 || strings.ToLower(service.query.MinLevel) != "debug" {
		t.Fatalf("query = %#v", service.query)
	}
	if service.query.Fields["event"] != "tool_call" || service.query.Fields["risk"] != "high" || service.query.Fields["tool"] != "shell" {
		t.Fatalf("fields = %#v", service.query.Fields)
	}
	if !strings.Contains(result.Content, "tool_call") || !strings.Contains(result.Content, "tool=shell") {
		t.Fatalf("content = %q", result.Content)
	}
}

func TestLogCommandDefaultsToDebugAndFiveEntries(t *testing.T) {
	service := &fakeLogService{entries: []logging.LogEntry{{
		Time:    time.Date(2026, 6, 3, 15, 0, 0, 0, time.Local),
		Level:   "INFO",
		Message: "started",
		Fields:  map[string]string{},
	}}}
	result, err := NewLog(Deps{Logs: service}).Handle(context.Background(), command.Request{})
	if err != nil {
		t.Fatalf("log handle: %v", err)
	}
	if service.query.Prefix != "elbot" || service.query.Limit != defaultLogListLimit || strings.ToLower(service.query.MinLevel) != "debug" {
		t.Fatalf("query = %#v", service.query)
	}
	if !strings.Contains(result.Content, "runtime logs:") || !strings.Contains(result.Content, "started") {
		t.Fatalf("content = %q", result.Content)
	}
}

func TestLogCommandShowsStructuredRuntimeFields(t *testing.T) {
	service := &fakeLogService{entries: []logging.LogEntry{{
		Time:    time.Date(2026, 6, 6, 19, 19, 1, 0, time.Local),
		Level:   "INFO",
		Message: "hook triggered",
		Fields: map[string]string{
			"point":          "platform.connected",
			"hook":           "notify_qqonebot_connected",
			"priority":       "1000",
			"order":          "1",
			"mode":           "run",
			"before_content": "secret before",
			"after_content":  "secret after",
			"raw_content":    "secret raw",
		},
	}}}
	result, err := NewLog(Deps{Logs: service}).Handle(context.Background(), command.Request{})
	if err != nil {
		t.Fatalf("log handle: %v", err)
	}
	for _, want := range []string{"hook triggered", "point=platform.connected", "hook=notify_qqonebot_connected", "priority=1000", "order=1", "mode=run"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("content missing %q:\n%s", want, result.Content)
		}
	}
	for _, hidden := range []string{"secret before", "secret after", "secret raw", "before_content", "after_content", "raw_content"} {
		if strings.Contains(result.Content, hidden) {
			t.Fatalf("content should hide %q:\n%s", hidden, result.Content)
		}
	}
}

func TestLogCommandParsesTypeFiltersAndQuotedContains(t *testing.T) {
	service := &fakeLogService{entries: []logging.LogEntry{{Message: "user input", Fields: map[string]string{"event": "user_message", "text": "hello world"}}}}
	_, err := NewLog(Deps{Logs: service}).Handle(context.Background(), command.Request{Args: `-u --contains "hello world"`})
	if err != nil {
		t.Fatalf("log handle: %v", err)
	}
	if service.query.Fields["event"] != "user_message" || service.query.Contains != "hello world" || service.query.Raw {
		t.Fatalf("query = %#v", service.query)
	}

	_, err = NewLog(Deps{Logs: service}).Handle(context.Background(), command.Request{Args: `-a`})
	if err != nil {
		t.Fatalf("log handle -a: %v", err)
	}
	if service.query.Fields["event"] != "assistant_message" {
		t.Fatalf("query = %#v", service.query)
	}

	_, err = NewLog(Deps{Logs: service}).Handle(context.Background(), command.Request{Args: `-t`})
	if err != nil {
		t.Fatalf("log handle -t: %v", err)
	}
	if service.query.Fields["event"] != "tool_call" {
		t.Fatalf("query = %#v", service.query)
	}

	_, err = NewLog(Deps{Logs: service}).Handle(context.Background(), command.Request{Args: `--hook`})
	if err != nil {
		t.Fatalf("log handle --hook: %v", err)
	}
	if len(service.query.FieldExists) != 1 || service.query.FieldExists[0] != "hook" {
		t.Fatalf("query = %#v", service.query)
	}
}

func TestAuditCommandParsesTypeFiltersAndQuotedContains(t *testing.T) {
	service := &fakeLogService{entries: []logging.LogEntry{{Message: "audit event", Fields: map[string]string{"event": "tool_call", "arguments": "hello world"}}}}
	_, err := NewAudit(Deps{Logs: service}).Handle(context.Background(), command.Request{Args: `-t --contains "hello world"`})
	if err != nil {
		t.Fatalf("audit handle: %v", err)
	}
	if service.query.Fields["event"] != "tool_call" || service.query.Contains != "hello world" || service.query.Raw {
		t.Fatalf("query = %#v", service.query)
	}

	_, err = NewAudit(Deps{Logs: service}).Handle(context.Background(), command.Request{Args: `--hook`})
	if err != nil {
		t.Fatalf("audit handle --hook: %v", err)
	}
	if service.query.Fields["event"] != "hook" {
		t.Fatalf("query = %#v", service.query)
	}
}

func TestAuditCommandMapsOldMessageEventAliases(t *testing.T) {
	service := &fakeLogService{entries: []logging.LogEntry{{Message: "audit event", Fields: map[string]string{"event": "user_message"}}}}
	_, err := NewAudit(Deps{Logs: service}).Handle(context.Background(), command.Request{Args: `--event user_input`})
	if err != nil {
		t.Fatalf("audit handle user_input: %v", err)
	}
	if service.query.Fields["event"] != "user_message" {
		t.Fatalf("query = %#v", service.query)
	}

	_, err = NewAudit(Deps{Logs: service}).Handle(context.Background(), command.Request{Args: `--event assistant_output`})
	if err != nil {
		t.Fatalf("audit handle assistant_output: %v", err)
	}
	if service.query.Fields["event"] != "assistant_message" {
		t.Fatalf("query = %#v", service.query)
	}
}

func TestLogAndAuditCommandsCompleteOptionsAndValues(t *testing.T) {
	logCmd := NewLog(Deps{}).(command.Completer)
	got := logCmd.Complete(context.Background(), command.CompletionRequest{Raw: "/log --le", Prefix: "/", Name: "log", Args: "--le", Cursor: len("/log --le")})
	if len(got) != 1 || got[0].Text != "--level" {
		t.Fatalf("log option Complete = %#v", got)
	}
	got = logCmd.Complete(context.Background(), command.CompletionRequest{Raw: "/log --level w", Prefix: "/", Name: "log", Args: "--level w", Cursor: len("/log --level w")})
	if len(got) != 1 || got[0].Text != "warn" || got[0].Kind != "log_level" {
		t.Fatalf("log level Complete = %#v", got)
	}

	auditCmd := NewAudit(Deps{Tools: &fakeToolService{infos: []tool.Info{{Name: "shell"}, {Name: "web_search"}}}}).(command.Completer)
	got = auditCmd.Complete(context.Background(), command.CompletionRequest{Raw: "/audit --risk h", Prefix: "/", Name: "audit", Args: "--risk h", Cursor: len("/audit --risk h")})
	if len(got) != 1 || got[0].Text != "high" {
		t.Fatalf("audit risk Complete = %#v", got)
	}
	got = auditCmd.Complete(context.Background(), command.CompletionRequest{Raw: "/audit --tool sh", Prefix: "/", Name: "audit", Args: "--tool sh", Cursor: len("/audit --tool sh")})
	if len(got) != 1 || got[0].Text != "shell" {
		t.Fatalf("audit tool Complete = %#v", got)
	}
}

func TestLogCommandHelpAndRawDebug(t *testing.T) {
	service := &fakeLogService{}
	result, err := NewLog(Deps{Logs: service}).Handle(context.Background(), command.Request{Args: `-h`})
	if err != nil {
		t.Fatalf("log help: %v", err)
	}
	if !strings.Contains(result.Content, "command: log") {
		t.Fatalf("content = %q", result.Content)
	}

	service.entries = []logging.LogEntry{{Message: "debug", Level: "DEBUG", Raw: "raw debug"}}
	result, err = NewLog(Deps{Logs: service}).Handle(context.Background(), command.Request{Args: `-d`})
	if err != nil {
		t.Fatalf("log raw: %v", err)
	}
	if !service.query.Raw || !strings.Contains(result.Content, "raw debug") {
		t.Fatalf("query = %#v content = %q", service.query, result.Content)
	}
}

func TestLogCommandDebugShowsRawEntries(t *testing.T) {
	raw := `time="2026-06-03 15:00:00" level=DEBUG msg="openai chat request" body="{big request}" extra=value`
	service := &fakeLogService{entries: []logging.LogEntry{{
		Time:    time.Date(2026, 6, 3, 15, 0, 0, 0, time.Local),
		Level:   "DEBUG",
		Message: "openai chat request",
		Fields:  map[string]string{"body": "{big request}"},
		Raw:     raw,
	}}}
	result, err := NewLog(Deps{Logs: service}).Handle(context.Background(), command.Request{Args: "--level debug"})
	if err != nil {
		t.Fatalf("log handle: %v", err)
	}
	if strings.ToLower(service.query.MinLevel) != "debug" {
		t.Fatalf("MinLevel = %q", service.query.MinLevel)
	}
	if !strings.Contains(result.Content, raw) {
		t.Fatalf("content = %q", result.Content)
	}
}

func TestLogCommandDebugShowsParsedBodyJSON(t *testing.T) {
	raw := `time="2026-06-03 15:00:00" level=DEBUG msg="openai chat request" model=test body_json="{\"messages\":[{\"role\":\"user\",\"content\":\"hello\"}]}"`
	body := `{"messages":[{"role":"user","content":"hello"}]}`
	service := &fakeLogService{entries: []logging.LogEntry{{
		Time:    time.Date(2026, 6, 3, 15, 0, 0, 0, time.Local),
		Level:   "DEBUG",
		Message: "openai chat request",
		Fields:  map[string]string{"body_json": body},
		Raw:     raw,
	}}}
	result, err := NewLog(Deps{Logs: service}).Handle(context.Background(), command.Request{Args: "--level debug"})
	if err != nil {
		t.Fatalf("log handle: %v", err)
	}
	if strings.ToLower(service.query.MinLevel) != "debug" {
		t.Fatalf("MinLevel = %q", service.query.MinLevel)
	}
	if !strings.Contains(result.Content, "body_json:\n") || !strings.Contains(result.Content, body) {
		t.Fatalf("content = %q", result.Content)
	}
	if strings.Contains(result.Content, `body_json="{\"`) {
		t.Fatalf("content should not show escaped raw body_json:\n%s", result.Content)
	}
}

func TestAuditCommandFormatsLLMUsageTokens(t *testing.T) {
	service := &fakeLogService{entries: []logging.LogEntry{{
		Time:    time.Date(2026, 6, 12, 10, 12, 57, 0, time.Local),
		Level:   "DEBUG",
		Message: "audit event",
		Fields: map[string]string{
			"event":             "llm_usage",
			"session_id":        "sess-1",
			"provider":          "deepseek",
			"model":             "deepseek-v4-flash",
			"elapsed_ms":        "1234",
			"prompt_tokens":     "1000",
			"completion_tokens": "200",
			"total_tokens":      "1200",
			"cache_hit_tokens":  "750",
		},
	}}}
	result, err := NewAudit(Deps{Logs: service}).Handle(context.Background(), command.Request{Args: "--event llm_usage"})
	if err != nil {
		t.Fatalf("audit handle: %v", err)
	}
	for _, want := range []string{"llm_usage", "provider=deepseek", "model=deepseek-v4-flash", "elapsed_ms=1234", "tokens=1000/200/1200", "cache_hit=750/1000(75.0%)"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("content missing %q:\n%s", want, result.Content)
		}
	}
}

func TestAuditCommandDebugShowsRawEntries(t *testing.T) {
	raw := `time="2026-06-03 15:00:00" level=DEBUG msg="audit event" event=llm_usage prompt_tokens=123 completion_tokens=456`
	service := &fakeLogService{entries: []logging.LogEntry{{
		Time:    time.Date(2026, 6, 3, 15, 0, 0, 0, time.Local),
		Level:   "DEBUG",
		Message: "audit event",
		Fields:  map[string]string{"event": "llm_usage"},
		Raw:     raw,
	}}}
	result, err := NewAudit(Deps{Logs: service}).Handle(context.Background(), command.Request{Args: "--level debug"})
	if err != nil {
		t.Fatalf("audit handle: %v", err)
	}
	if strings.ToLower(service.query.MinLevel) != "debug" {
		t.Fatalf("MinLevel = %q", service.query.MinLevel)
	}
	if !strings.Contains(result.Content, raw) {
		t.Fatalf("content = %q", result.Content)
	}
}
