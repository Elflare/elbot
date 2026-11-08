package control

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"elbot/internal/events"
	"elbot/internal/logging"
)

type logStub struct {
	query  logging.LogQuery
	result logging.LogQueryResult
	calls  int
}

func (s *logStub) Query(_ context.Context, query logging.LogQuery) (logging.LogQueryResult, error) {
	s.query = query
	s.calls++
	result := s.result
	if query.Limit > 0 && len(result.Entries) > query.Limit {
		result.Entries = result.Entries[:query.Limit]
	}
	return result, nil
}

func TestLogJSONPrecisionAndRawSelection(t *testing.T) {
	provider := &logStub{result: logging.LogQueryResult{Entries: []logging.LogEntry{
		{Type: events.ContentJSON, Content: `{"value":9007199254740993}`, Identity: events.LogIdentity{SessionID: "s", RootRequestID: "r"}, Raw: "raw-json"},
		{Type: events.ContentText, Content: "plain text", Raw: "raw-text"},
	}, DamagedCount: 1, DamagedLines: []string{"damaged"}}}
	m, _ := testSupervisor(t, SupervisorDeps{Logs: provider})
	_, err := m.QueryLogs(testContext(false), QueryLogsRequest{Category: "audit"})
	requireCode(t, err, "permission_denied")
	if provider.calls != 0 {
		t.Fatal("unauthorized read reached log provider")
	}
	result, err := m.QueryLogs(testContext(true), QueryLogsRequest{Category: "audit", Query: logging.LogQuery{Prefix: "../../outside"}})
	if err != nil {
		t.Fatal(err)
	}
	if provider.query.Prefix != "audit" || len(result.DamagedLines) != 0 || result.Items[0].Raw != "" {
		t.Fatalf("query/result: %+v %+v", provider.query, result)
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `9007199254740993`) || !strings.Contains(string(data), `"root_request_id":"r"`) {
		t.Fatalf("lost JSON value or identity: %s", data)
	}
	result, err = m.QueryLogs(testContext(true), QueryLogsRequest{Category: "runtime", Query: logging.LogQuery{Raw: true}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Items[0].Raw != "raw-json" || len(result.DamagedLines) != 1 {
		t.Fatalf("raw data missing: %+v", result)
	}
	_, err = m.QueryLogs(testContext(true), QueryLogsRequest{Category: "../../outside"})
	requireCode(t, err, "invalid_arguments")
}

func usageEntry(model, prompt, total string) logging.LogEntry {
	return logging.LogEntry{Type: events.ContentJSON, Time: time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC), Identity: events.LogIdentity{SessionID: "s"}, Content: `{"provider":"p","model":"` + model + `","prompt_tokens":` + prompt + `,"completion_tokens":0,"total_tokens":` + total + `,"cache_hit_tokens":0,"elapsed_ms":1}`}
}

func TestUsageUnknownAndOverflowRemainNull(t *testing.T) {
	provider := &logStub{result: logging.LogQueryResult{Entries: []logging.LogEntry{usageEntry("m", "3", "10"), usageEntry("m", "null", "20"), usageEntry("other", "0", "9223372036854775807")}, DamagedCount: 2}}
	m, _ := testSupervisor(t, SupervisorDeps{Logs: provider})
	fields := map[string]string{"event": "wrong"}
	result, err := m.QueryUsage(testContext(true), QueryUsageRequest{Query: logging.LogQuery{Fields: fields}, GroupBy: "model", First: 1, Last: 3})
	if err != nil {
		t.Fatal(err)
	}
	if fields["event"] != "wrong" || provider.query.Fields["event"] != "llm_usage" || provider.query.Prefix != "audit" {
		t.Fatal("usage query mutated input or failed to fix event")
	}
	if result.Total.Calls != 3 || result.Total.PromptTokens != nil || result.Total.TotalTokens != nil || result.DamagedCount != 2 || result.RangeTruncated {
		t.Fatalf("totals: %+v", result)
	}
	if result.Total.CompletionTokens == nil || *result.Total.CompletionTokens != 0 {
		t.Fatal("explicit zero became unknown")
	}
	if len(result.Items) != 2 || result.Items[0].PromptTokens != nil || result.Items[0].TotalTokens == nil || *result.Items[0].TotalTokens != 30 {
		t.Fatalf("buckets: %+v", result.Items)
	}
	if result.Items[1].TotalTokens == nil || *result.Items[1].TotalTokens != math.MaxInt64 {
		t.Fatal("maximum valid count was lost")
	}
	selected, err := m.QueryUsage(testContext(true), QueryUsageRequest{GroupBy: "model", First: 1, Last: 1})
	if err != nil {
		t.Fatal(err)
	}
	if selected.Total.PromptTokens == nil || *selected.Total.PromptTokens != 3 || selected.Total.Calls != 1 {
		t.Fatalf("selected range: %+v", selected)
	}
	if provider.query.Limit != 1 || selected.AvailableCalls != 1 || selected.RangeTruncated {
		t.Fatal("single-call range queried more than one entry")
	}
	selected, err = m.QueryUsage(testContext(true), QueryUsageRequest{First: 2, Last: 5, GroupBy: "day"})
	if err != nil || !selected.RangeTruncated || selected.Total.Calls != 2 {
		t.Fatalf("truncation: %+v %v", selected, err)
	}
}

func TestLogQueriesForwardCallerOptions(t *testing.T) {
	provider := &logStub{}
	m, _ := testSupervisor(t, SupervisorDeps{Logs: provider})
	since := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	until := since.AddDate(0, 0, 7)
	query := logging.LogQuery{Limit: 100001, Days: 90, Since: &since, Until: &until, MinLevel: "warn", Fields: map[string]string{"session_id": "s"}, Events: []string{"chosen-event"}, ContentFields: map[string]string{"model": "m"}, ContentFieldContains: map[string][]string{"tools": {"read"}}, Contains: "record", ContentContains: "content", Raw: true}
	_, err := m.QueryLogs(testContext(true), QueryLogsRequest{Category: "runtime", Query: query})
	if err != nil {
		t.Fatal(err)
	}
	want := query
	want.Prefix = "runtime"
	if !reflect.DeepEqual(provider.query, want) {
		t.Fatalf("log query options changed: %+v", provider.query)
	}
	_, err = m.QueryUsage(testContext(true), QueryUsageRequest{Query: query, GroupBy: "session", First: 100000, Last: 100001})
	if err != nil {
		t.Fatal(err)
	}
	want.Prefix = "audit"
	want.Fields = map[string]string{"session_id": "s", "event": "llm_usage"}
	want.Events = nil
	if !reflect.DeepEqual(provider.query, want) || len(query.Fields) != 1 {
		t.Fatalf("usage query options changed: %+v", provider.query)
	}
}
