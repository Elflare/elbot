package execution

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"elbot/internal/events"
	"elbot/internal/operationresult"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"elbot/internal/llm"
)

func executePreparedELwispTestCall(t *testing.T, call llm.ToolCallRequest, resolved resolvedTool) executionResult {
	t.Helper()
	resolved.Source = toolshare.SourceKindELwisp
	manager := &Manager{}
	prepared, err := manager.prepareCall(t.Context(), resolved, call)
	if err != nil {
		t.Fatal(err)
	}
	return manager.executePrepared(t.Context(), call, resolved, prepared.Data, nil)
}

func TestExecuteELwispToolPostsArguments(t *testing.T) {
	var received elwispToolRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			t.Fatalf("method = %s", req.Method)
		}
		if err := json.NewDecoder(req.Body).Decode(&received); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":"ok"}`))
	}))
	defer server.Close()

	call := llm.ToolCallRequest{ID: "call-1", Name: "elwisp_watcher_server_status", Arguments: `{"detail":true}`}
	result := executePreparedELwispTestCall(t, call, resolvedTool{Discovered: &toolshare.DiscoveredTool{Name: "server_status", CanonicalName: call.Name, Source: toolshare.SourceKindELwisp, EventKey: "watcher/source/id", Endpoint: server.URL, TimeoutSeconds: 10, Risk: toolshare.RiskLow}})
	if result.Error != nil {
		t.Fatalf("executeELwispTool: %v", result.Error)
	}
	if got := llm.SegmentsContentText(result.Message.Segments); got != "ok" {
		t.Fatalf("content = %q", got)
	}
	if received.Tool != "server_status" || received.EventKey != "watcher/source/id" || string(received.Arguments) != `{"detail":true}` {
		t.Fatalf("received = %#v", received)
	}
}

func TestExecuteELwispToolReturnsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer server.Close()

	call := llm.ToolCallRequest{ID: "call-1", Name: "elwisp_watcher_server_status", Arguments: `{}`}
	result := executePreparedELwispTestCall(t, call, resolvedTool{Discovered: &toolshare.DiscoveredTool{Name: "server_status", CanonicalName: call.Name, Source: toolshare.SourceKindELwisp, Endpoint: server.URL, TimeoutSeconds: 10, Risk: toolshare.RiskLow}})
	if result.Error != nil || result.Result.Status != operationresult.Failed {
		t.Fatal("expected HTTP error")
	}
	if !result.ExecutionStarted || result.Error != nil {
		t.Fatalf("send facts = %#v", result)
	}
}

func TestELwispFailureMessageKeepsCompletedActions(t *testing.T) {
	body := strings.Repeat("雪🔥", 700) + "BUSINESS_TAIL"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(elwispToolResponse{Content: body, Error: "second action failed"})
	}))
	defer server.Close()
	result := executePreparedELwispTestCall(t, llm.ToolCallRequest{Name: "remote", Arguments: "{}"}, resolvedTool{Discovered: &toolshare.DiscoveredTool{Endpoint: server.URL, TimeoutSeconds: 10, Risk: toolshare.RiskLow}})
	want := "ELwisp tool error: second action failed\n" + body
	if result.Error != nil || !result.ExecutionStarted || result.Result.Status != operationresult.Failed || result.Result.Data != nil || result.Result.Message != want || llm.SegmentsContentText(result.Message.Segments) != want || result.Summary != "" {
		t.Fatalf("lost failure context: %+v", result)
	}
}

func TestPrepareELwispToolFixesRequestWithoutSending(t *testing.T) {
	var calls atomic.Int32
	var received elwispToolRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		if err := json.NewDecoder(req.Body).Decode(&received); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"content":"original endpoint"}`))
	}))
	defer server.Close()
	call := llm.ToolCallRequest{ID: "one", Name: "remote", Arguments: `{"value":1}`}
	resolved := resolvedTool{Source: toolshare.SourceKindELwisp, Discovered: &toolshare.DiscoveredTool{Name: "original", EventKey: "original/event", Endpoint: server.URL, TimeoutSeconds: 10, Risk: toolshare.RiskLow}}
	manager := &Manager{}
	prepared, err := manager.prepareCall(t.Context(), resolved, call)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("preparation sent a request")
	}
	if prepared.Data.RiskAssessment().Level != toolshare.RiskLow || len(prepared.Data.Details()) != 0 {
		t.Fatalf("risk=%+v details=%+v", prepared.Data.RiskAssessment(), prepared.Data.Details())
	}
	resolved.Discovered.Name = "changed"
	resolved.Discovered.EventKey = "changed/event"
	resolved.Discovered.Endpoint = "http://invalid.invalid"
	resolved.Discovered.TimeoutSeconds = 1
	started := false
	result := manager.executePrepared(t.Context(), call, resolved, prepared.Data, func() { started = true })
	if result.Error != nil || !result.ExecutionStarted || !started || calls.Load() != 1 || result.Result.Data.Content != "original endpoint" {
		t.Fatalf("result=%+v started=%v calls=%d", result, started, calls.Load())
	}
	if received.Tool != "original" || received.EventKey != "original/event" || string(received.Arguments) != `{"value":1}` {
		t.Fatalf("request changed after preparation: %+v", received)
	}
}

func TestPrepareELwispToolRejectsInvalidRequestBeforeSending(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { calls.Add(1) }))
	defer server.Close()
	for _, scenario := range []string{"arguments", "endpoint"} {
		t.Run(scenario, func(t *testing.T) {
			call := llm.ToolCallRequest{Name: "remote", Arguments: `{}`}
			resolved := resolvedTool{Discovered: &toolshare.DiscoveredTool{Endpoint: server.URL, TimeoutSeconds: 10, Risk: toolshare.RiskLow}}
			if scenario == "arguments" {
				call.Arguments = `{invalid`
			} else {
				resolved.Discovered.Endpoint = "http://[invalid"
			}
			prepared, err := prepareELwispTool(call, resolved)
			if (scenario == "endpoint" && err == nil) || (scenario == "arguments" && (err != nil || prepared.Status != operationresult.InvalidArguments)) || prepared.Data != nil || calls.Load() != 0 {
				t.Fatalf("prepared=%v error=%v calls=%d", prepared, err, calls.Load())
			}
		})
	}
}

func TestPreparedELwispCallChecksRequestAndCancellationBeforeSending(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { calls.Add(1) }))
	defer server.Close()
	for _, scenario := range []string{"arguments", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			call := llm.ToolCallRequest{ID: "one", Name: "remote", Arguments: `{}`}
			resolved := resolvedTool{Source: toolshare.SourceKindELwisp, Discovered: &toolshare.DiscoveredTool{Endpoint: server.URL, TimeoutSeconds: 10, Risk: toolshare.RiskLow}}
			prepared, err := prepareELwispTool(call, resolved)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if scenario == "arguments" {
				call.Arguments = `{"changed":true}`
			} else {
				cancel()
			}
			result := (&Manager{ManagerDeps: ManagerDeps{Log: func(context.Context, events.LogRecord) {}}}).executePrepared(ctx, call, resolved, prepared.Data, nil)
			if result.Error == nil || result.ExecutionStarted || calls.Load() != 0 {
				t.Fatalf("result=%+v calls=%d", result, calls.Load())
			}
		})
	}
}
