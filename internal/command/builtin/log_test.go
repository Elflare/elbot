package builtin

import (
	"context"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"elbot/internal/command"
	"elbot/internal/events"
	"elbot/internal/logging"
	"elbot/internal/tool"
)

type fakeLogService struct {
	query   logging.LogQuery
	entries []logging.LogEntry
}

func (s *fakeLogService) Query(ctx context.Context, query logging.LogQuery) ([]logging.LogEntry, error) {
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

func TestElwispCommandDefaultsToElnisDebugAndFiveEntries(t *testing.T) {
	service := &fakeLogService{entries: []logging.LogEntry{{
		Time:    time.Date(2026, 6, 18, 12, 0, 0, 0, time.Local),
		Level:   "INFO",
		Message: "elnis event accepted",
		Fields:  map[string]string{"elwisp_name": "server-watchdog", "source": "minecraft-main", "source_id": "cpu-alert-001", "mode": "llm"},
	}}}
	result, err := NewElwisp(Deps{Logs: service}).Handle(context.Background(), command.Request{})
	if err != nil {
		t.Fatalf("elwisp handle: %v", err)
	}
	if service.query.Prefix != "elnis" || service.query.Limit != defaultLogListLimit || service.query.Days != 1 || strings.ToLower(service.query.MinLevel) != "debug" {
		t.Fatalf("query = %#v", service.query)
	}
	for _, want := range []string{"elwisp logs:", "elnis event accepted", "elwisp=server-watchdog", "source=minecraft-main", "id=cpu-alert-001", "mode=llm"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("content missing %q:\n%s", want, result.Content)
		}
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

func TestElwispCommandParsesFiltersAndPositionalName(t *testing.T) {
	service := &fakeLogService{entries: []logging.LogEntry{{Message: "elnis llm completed", Fields: map[string]string{"elwisp_name": "server-watchdog"}}}}
	_, err := NewElwisp(Deps{Logs: service}).Handle(context.Background(), command.Request{Args: `server-watchdog --source minecraft-main --id cpu-alert-001 --mode llm --event-key server-watchdog/minecraft-main/cpu-alert-001 --event-id evt-1 --token home -n 3 --since 2h`})
	if err != nil {
		t.Fatalf("elwisp handle: %v", err)
	}
	if service.query.Fields["elwisp_name"] != "server-watchdog" || service.query.Fields["source"] != "minecraft-main" || service.query.Fields["source_id"] != "cpu-alert-001" || service.query.Fields["mode"] != "llm" {
		t.Fatalf("fields = %#v", service.query.Fields)
	}
	if service.query.Fields["event_key"] != "server-watchdog/minecraft-main/cpu-alert-001" || service.query.Fields["event_id"] != "evt-1" || service.query.Fields["token_name"] != "home" {
		t.Fatalf("fields = %#v", service.query.Fields)
	}
	if service.query.Limit != 3 || service.query.Since == nil {
		t.Fatalf("query = %#v", service.query)
	}

	_, err = NewElwisp(Deps{Logs: service}).Handle(context.Background(), command.Request{Args: `foo --name bar`})
	if err == nil {
		t.Fatal("conflicting elwisp names should fail")
	}
}

func TestElwispCommandParsesTagFilters(t *testing.T) {
	service := &fakeLogService{entries: []logging.LogEntry{{
		Time:    time.Date(2026, 6, 18, 12, 0, 0, 0, time.Local),
		Level:   "INFO",
		Message: "elnis event accepted",
		Fields:  map[string]string{"elwisp_name": "watcher", "tags": "[\"windows\",\"onedrive\",\"watchdog\"]"},
	}}}
	result, err := NewElwisp(Deps{Logs: service}).Handle(context.Background(), command.Request{Args: `--tag onedrive --tag watchdog`})
	if err != nil {
		t.Fatalf("elwisp handle: %v", err)
	}
	wants := service.query.FieldContains["tags"]
	if len(wants) != 2 || wants[0] != "\"onedrive\"" || wants[1] != "\"watchdog\"" || service.query.Contains != "" {
		t.Fatalf("query = %#v", service.query)
	}
	if !strings.Contains(result.Content, `tags=["windows","onedrive","watchdog"]`) {
		t.Fatalf("content = %q", result.Content)
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

	service.entries = []logging.LogEntry{{
		Message: "system prompt",
		Fields:  map[string]string{"event": "system_message", "first_system_message_json": `{"role":"system","content":"You are ElBot."}`},
	}}
	for _, arg := range []string{"-s", "--system"} {
		result, err := NewLog(Deps{Logs: service}).Handle(context.Background(), command.Request{Args: arg})
		if err != nil {
			t.Fatalf("log handle %s: %v", arg, err)
		}
		if service.query.Fields["event"] != "system_message" {
			t.Fatalf("query = %#v", service.query)
		}
		if !strings.Contains(result.Content, "system prompt") || !strings.Contains(result.Content, "first_system_message_json") {
			t.Fatalf("content = %q", result.Content)
		}
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
	if service.query.Fields["module"] != "hook" || len(service.query.FieldExists) != 0 {
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
	if service.query.Fields["module"] != "hook" || service.query.Fields["event"] != "" {
		t.Fatalf("query = %#v", service.query)
	}
}

func TestAuditCommandPreservesLiteralEventNames(t *testing.T) {
	service := &fakeLogService{entries: []logging.LogEntry{{Message: "audit event", Fields: map[string]string{"event": "user_message"}}}}
	_, err := NewAudit(Deps{Logs: service}).Handle(context.Background(), command.Request{Args: `--event user_input`})
	if err != nil {
		t.Fatalf("audit handle user_input: %v", err)
	}
	if service.query.Fields["event"] != "user_input" {
		t.Fatalf("query = %#v", service.query)
	}

	_, err = NewAudit(Deps{Logs: service}).Handle(context.Background(), command.Request{Args: `--event assistant_output`})
	if err != nil {
		t.Fatalf("audit handle assistant_output: %v", err)
	}
	if service.query.Fields["event"] != "assistant_output" {
		t.Fatalf("query = %#v", service.query)
	}
}

func TestAuditCommandRemovedOptionsAndHookCombinations(t *testing.T) {
	service := &fakeLogService{}
	for _, option := range []string{"-u", "-a"} {
		_, err := NewAudit(Deps{Logs: service}).Handle(context.Background(), command.Request{Args: option})
		if err == nil || !strings.Contains(err.Error(), "unknown option: "+option) {
			t.Fatalf("%s: %v", option, err)
		}
	}
	for _, args := range []string{"--hook --event hook_tool_call", "--event hook_tool_call --hook"} {
		_, err := NewAudit(Deps{Logs: service}).Handle(context.Background(), command.Request{Args: args})
		if err != nil || service.query.Fields["module"] != "hook" || service.query.Fields["event"] != "hook_tool_call" {
			t.Fatalf("%s: query=%+v err=%v", args, service.query, err)
		}
	}
	cmd := NewAudit(Deps{})
	help, err := cmd.Handle(context.Background(), command.Request{Args: "--help"})
	if err != nil {
		t.Fatal(err)
	}
	for _, option := range []string{"  -u", "  -a"} {
		if strings.Contains(help.Content, option) {
			t.Fatalf("obsolete help: %s", help.Content)
		}
	}
	for _, tc := range []struct {
		raw          string
		want, absent []string
	}{
		{"/audit ", []string{"-t", "--hook"}, []string{"-u", "-a"}},
		{"/audit --event ", []string{"llm_usage", "hook_tool_call", "session_naming_failed"}, []string{"user_input", "assistant_output", "user_message", "assistant_message", "hook", "hook_tool_error", "hook.tool_call"}},
	} {
		completions := cmd.(command.Completer).Complete(context.Background(), command.CompletionRequest{Raw: tc.raw, Prefix: "/", Name: "audit", Cursor: len(tc.raw)})
		var names []string
		for _, completion := range completions {
			names = append(names, completion.Text)
		}
		for _, want := range tc.want {
			if !slices.Contains(names, want) {
				t.Fatalf("%q missing %q: %v", tc.raw, want, names)
			}
		}
		for _, absent := range tc.absent {
			if slices.Contains(names, absent) {
				t.Fatalf("%q retained %q: %v", tc.raw, absent, names)
			}
		}
	}
}

func TestLogCommandsQueryGlobalRecords(t *testing.T) {
	// Global subscriptions are intentionally serial; each center is closed
	// before another level's fixture starts.
	for _, level := range []string{"info", "debug", "warn", "error"} {
		t.Run(level, func(t *testing.T) {
			center, err := logging.NewManager(level, filepath.Join(t.TempDir(), "sessions.sqlite"), 1)
			if err != nil {
				t.Fatal(err)
			}
			closeCenter := func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := center.Close(ctx); err != nil {
					t.Error(err)
				}
			}
			t.Cleanup(closeCenter)
			publish := func(record events.LogRecord) {
				t.Helper()
				if err := events.EmitLog(context.Background(), record); err != nil {
					t.Fatal(err)
				}
			}
			body := strings.Repeat("中文正文", 100) + "TAIL_BODY"
			for _, name := range []string{"user_message", "assistant_message", "system_message"} {
				publish(events.LogRecord{Category: events.LogRuntime, Level: slog.LevelInfo, Name: name, Module: "agent", Summary: name + ": " + body, Detail: body})
			}
			publish(events.LogRecord{Category: events.LogAudit, Level: slog.LevelInfo, Name: "llm_usage", Module: "agent", Summary: "llm usage", Fields: []slog.Attr{
				slog.String("provider", "test"), slog.String("model", "model"), slog.Int("prompt_tokens", 10), slog.Int("completion_tokens", 5), slog.Int("total_tokens", 15),
			}})
			publish(events.LogRecord{Category: events.LogAudit, Level: slog.LevelWarn, Name: "llm_error", Module: "agent", Summary: "upstream failed", Detail: "upstream diagnostic marker"})
			for _, module := range []string{"hook", "agent", ""} {
				for _, category := range []events.LogCategory{events.LogRuntime, events.LogAudit} {
					// A hook field alone must not imply module=hook.
					publish(events.LogRecord{Category: category, Level: slog.LevelError, Name: "hook_tool_call", Module: module, Summary: "hook tool call", Fields: []slog.Attr{slog.String("hook", "sample")}})
				}
			}
			closeCenter()
			deps := Deps{Logs: logging.Reader{Dir: center.LogDir()}}
			query := func(handler command.Handler, args string) string {
				t.Helper()
				result, err := handler.Handle(context.Background(), command.Request{Args: args})
				if err != nil {
					t.Fatal(err)
				}
				return result.Content
			}
			for _, filter := range []string{"-u", "-a", "--system"} {
				summary := query(NewLog(deps), filter)
				raw := query(NewLog(deps), filter+" -d")
				if strings.Contains(summary, "TAIL_BODY") {
					t.Fatalf("summary exposed full detail: %s", summary)
				}
				if level == "info" || level == "debug" {
					if !strings.Contains(summary, "中文正文") || !strings.Contains(summary, "truncated") {
						t.Fatalf("missing summary: %s", summary)
					}
				} else if summary != "no log entries found" {
					t.Fatalf("runtime filtering: %s", summary)
				}
				if strings.Contains(raw, "TAIL_BODY") != (level == "debug") {
					t.Fatalf("detail at %s: %s", level, raw)
				}
			}
			for _, handler := range []command.Handler{NewLog(deps), NewAudit(deps)} {
				content := query(handler, "--hook")
				if strings.Count(content, "module=hook") != 1 || strings.Count(content, "hook_tool_call") != 1 {
					t.Fatalf("hook selection: %s", content)
				}
			}
			content := query(NewAudit(deps), "--hook --event hook_tool_call")
			if !strings.Contains(content, "module=hook") {
				t.Fatalf("combined hook selection: %s", content)
			}
			if query(NewAudit(deps), "--hook --event llm_usage") != "no log entries found" {
				t.Fatal("hook and event filters did not intersect")
			}
			if content := query(NewAudit(deps), "--event llm_error -d"); !strings.Contains(content, "upstream diagnostic marker") {
				t.Fatalf("lost upstream audit: %s", content)
			}
			if content := query(NewUsage(deps), ""); !strings.Contains(content, "prompt: 10 | completion: 5 | total: 15") {
				t.Fatalf("lost usage at %s: %s", level, content)
			}
		})
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
	got = logCmd.Complete(context.Background(), command.CompletionRequest{Raw: "/log --sy", Prefix: "/", Name: "log", Args: "--sy", Cursor: len("/log --sy")})
	if len(got) != 1 || got[0].Text != "--system" {
		t.Fatalf("log system Complete = %#v", got)
	}

	elwispCmd := NewElwisp(Deps{}).(command.Completer)
	got = elwispCmd.Complete(context.Background(), command.CompletionRequest{Raw: "/elwisp --mo", Prefix: "/", Name: "elwisp", Args: "--mo", Cursor: len("/elwisp --mo")})
	if len(got) != 1 || got[0].Text != "--mode" {
		t.Fatalf("elwisp option Complete = %#v", got)
	}
	got = elwispCmd.Complete(context.Background(), command.CompletionRequest{Raw: "/elwisp --mode l", Prefix: "/", Name: "elwisp", Args: "--mode l", Cursor: len("/elwisp --mode l")})
	if len(got) != 1 || got[0].Text != "llm" || got[0].Kind != "elnis_mode" {
		t.Fatalf("elwisp mode Complete = %#v", got)
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
	if !strings.Contains(result.Content, "-s, --system") {
		t.Fatalf("log help missing system filter: %q", result.Content)
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

func TestElwispCommandDebugShowsRawEntries(t *testing.T) {
	raw := `time="2026-06-18 12:00:00" level=DEBUG msg="elnis llm queued" elwisp_name=server-watchdog source=minecraft-main`
	service := &fakeLogService{entries: []logging.LogEntry{{
		Time:    time.Date(2026, 6, 18, 12, 0, 0, 0, time.Local),
		Level:   "DEBUG",
		Message: "elnis llm queued",
		Fields:  map[string]string{"elwisp_name": "server-watchdog"},
		Raw:     raw,
	}}}
	result, err := NewElwisp(Deps{Logs: service}).Handle(context.Background(), command.Request{Args: "-d"})
	if err != nil {
		t.Fatalf("elwisp handle: %v", err)
	}
	if !service.query.Raw || !strings.Contains(result.Content, raw) {
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
		Level:   "INFO",
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
