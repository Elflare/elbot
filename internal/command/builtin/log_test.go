package builtin

import (
	"context"
	"reflect"
	"strings"
	"testing"

	command "elbot/internal/command/share"
	"elbot/internal/control"
	"elbot/internal/tool"
	toolshare "elbot/internal/tool/share"
)

type logControlStub struct {
	command.ControlCapabilities
	request control.QueryLogsRequest
	result  control.QueryLogsResult
	infos   []tool.ToolInfo
}

func (s *logControlStub) QueryLogs(_ context.Context, req control.QueryLogsRequest) (control.QueryLogsResult, error) {
	s.request = req
	return s.result, nil
}
func (s *logControlStub) QueryTools(context.Context, tool.ToolQuery) ([]tool.ToolInfo, error) {
	return s.infos, nil
}

func TestAuditCommandParsesFiltersAndReturnsRawControlResult(t *testing.T) {
	want := control.QueryLogsResult{Items: []control.LogData{{EventName: "tool_call_completed", Content: []byte(`{"count":9007199254740993}`)}}}
	service := &logControlStub{result: want}
	result, err := NewAudit(Deps{CommandText: testCommandText, Control: service}).Handle(t.Context(), command.Request{Args: "--event tool_call_completed --risk high --tool shell -n 3 --days 2 --raw"})
	if err != nil || result == nil || !reflect.DeepEqual(result.Data, want) {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	query := service.request
	if query.Category != "audit" || query.Query.Prefix != "audit" || query.Query.Limit != 3 || query.Query.Days != 2 || !query.Query.Raw || query.Query.Fields["event"] != "tool_call_completed" || query.Query.ContentFields["risk"] != "high" || query.Query.ContentFields["tool"] != "shell" {
		t.Fatalf("request=%+v", query)
	}
}

func TestLogCommandsUseCategoryDefaultsAndRejectInvalidFilters(t *testing.T) {
	for _, tc := range []struct {
		name string
		new  func(Deps) command.Handler
		cat  string
		args string
	}{
		{name: "runtime", new: NewLog, cat: "runtime"},
		{name: "elwisp", new: NewElwisp, cat: "elnis", args: "server-watchdog --source minecraft --mode llm"},
	} {
		service := &logControlStub{}
		_, err := tc.new(Deps{CommandText: testCommandText, Control: service}).Handle(t.Context(), command.Request{Args: tc.args})
		if err != nil {
			t.Fatal(err)
		}
		if service.request.Category != tc.cat || service.request.Query.Limit != defaultLogListLimit || service.request.Query.Days != 1 || service.request.Query.MinLevel != "debug" {
			t.Fatalf("%s request=%+v", tc.name, service.request)
		}
	}
	if _, err := NewAudit(Deps{CommandText: testCommandText, Control: &logControlStub{}}).Handle(t.Context(), command.Request{Args: "--run_source other"}); err == nil {
		t.Fatal("invalid run source accepted")
	}
}

func TestLogQueryParsingAndCompletion(t *testing.T) {
	query, err := parseRuntimeLogQuery(`-u --contains "hello world" --system --raw -n 7 --since 2h`)
	if err != nil || !query.Raw || query.Limit != 7 || query.Fields["event"] != "system_message" || query.Contains != "hello world" || query.Since == nil {
		t.Fatalf("query=%+v err=%v", query, err)
	}
	query, err = parseElwispLogQuery("watchdog --source minecraft --tag prod --tag alert --mode llm")
	if err != nil || query.ContentFields["elwisp_name"] != "watchdog" || query.ContentFields["source"] != "minecraft" || strings.Join(query.ContentFieldContains["tags"], ",") != "prod,alert" || query.ContentFields["mode"] != "llm" {
		t.Fatalf("elwisp query=%+v err=%v", query, err)
	}
	for _, args := range []string{"--run_source", "--run_source other", "--since invalid"} {
		if _, err := parseAuditQuery(args); err == nil {
			t.Fatalf("parseAuditQuery(%q) succeeded", args)
		}
	}
	service := &logControlStub{infos: []tool.ToolInfo{{Definition: toolshare.Info{Name: "shell"}}}}
	completer := NewAudit(Deps{CommandText: testCommandText, Control: service}).(command.Completer)
	got := completer.Complete(t.Context(), command.CompletionRequest{Raw: "/audit --tool sh", Prefix: "/", Name: "audit", Args: "--tool sh", Cursor: len("/audit --tool sh")})
	if len(got) != 1 || got[0].Text != "shell" {
		t.Fatalf("tool completion=%#v", got)
	}
}

func TestAuditEventSelectorsReplaceEachOther(t *testing.T) {
	for _, tc := range []struct{ args, events, event string }{
		{"-t", "tool_call_prepared,tool_call_completed", ""},
		{"--event llm_usage -t", "tool_call_prepared,tool_call_completed", ""},
		{"-t --event llm_usage", "", "llm_usage"},
	} {
		query, err := parseAuditQuery(tc.args)
		if err != nil || strings.Join(query.Events, ",") != tc.events || query.Fields["event"] != tc.event {
			t.Fatalf("args=%q query=%+v err=%v", tc.args, query, err)
		}
	}
}
