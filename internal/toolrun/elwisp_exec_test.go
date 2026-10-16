package toolrun

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"elbot/internal/events"
	"elbot/internal/llm"
)

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
	result := executeELwispTool(context.Background(), call, ResolvedTool{Cached: &CachedTool{Name: "server_status", CanonicalName: call.Name, Source: SourceKindELwisp, EventKey: "watcher/source/id", Endpoint: server.URL}}, nil)
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
	result := executeELwispTool(context.Background(), call, ResolvedTool{Cached: &CachedTool{Name: "server_status", CanonicalName: call.Name, Source: SourceKindELwisp, Endpoint: server.URL}}, nil)
	if result.Error == nil {
		t.Fatal("expected HTTP error")
	}
	if !result.ExecutionStarted || result.ResultStatus != events.ResultFailed {
		t.Fatalf("send facts = %#v", result)
	}
}

func TestExecuteELwispToolPartialAndPreparationFacts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"content":"first action saved","error":"second action failed"}`))
	}))
	defer server.Close()
	result := executeELwispTool(context.Background(), llm.ToolCallRequest{Name: "remote", Arguments: `{}`}, ResolvedTool{Cached: &CachedTool{Endpoint: server.URL}}, nil)
	if result.Error == nil || !result.ExecutionStarted || result.ResultStatus != events.ResultFailed || result.Result == nil || result.Result.Content != "first action saved" {
		t.Fatalf("partial facts = %#v", result)
	}
	result = executeELwispTool(context.Background(), llm.ToolCallRequest{Name: "remote", Arguments: `{invalid`}, ResolvedTool{Cached: &CachedTool{Endpoint: server.URL}}, nil)
	if result.Error == nil || result.ExecutionStarted || result.ResultStatus != events.ResultFailed {
		t.Fatalf("preparation facts = %#v", result)
	}
}

func TestELwispLogKeepsErrorAheadOfLongPartialResult(t *testing.T) {
	body := strings.Repeat("雪🔥", 700) + "BUSINESS_TAIL"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(elwispToolResponse{Content: body, Error: "second action failed"})
	}))
	defer server.Close()
	result := executeELwispTool(t.Context(), llm.ToolCallRequest{Name: "remote", Arguments: `{}`}, ResolvedTool{Cached: &CachedTool{Endpoint: server.URL}}, nil)
	if result.Error == nil || result.Result == nil || result.Result.Content != body || llm.SegmentsContentText(result.Message.Segments) != body {
		t.Fatalf("partial business result changed: %+v", result)
	}
	if !strings.HasPrefix(result.LogResult.Content, "ELwisp tool error: second action failed\n") || len([]rune(result.LogResult.Content)) != 1024 || !strings.HasSuffix(result.LogResult.Content, "…") {
		t.Fatalf("log=%q", result.LogResult.Content)
	}
}
