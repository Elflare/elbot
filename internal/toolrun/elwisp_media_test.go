package toolrun

import (
	"context"
	"elbot/internal/events"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/llm"
)

func TestExternalElwispForwardsArguments(t *testing.T) {
	ctx := context.Background()
	const mediaID = "media:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var received elwispToolRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(`{"content":"ok"}`))
	}))
	defer server.Close()
	manager := NewManager(nil, nil)
	args, _ := json.Marshal(map[string]any{"source": mediaID, "media_inputs": []map[string]string{{"media": mediaID}}})
	call := llm.ToolCallRequest{ID: "call", Name: "external", Arguments: string(args)}
	resolved := ResolvedTool{ResultStatus: events.ResultSucceeded, Source: SourceKindELwisp, Cached: &CachedTool{Name: "external", Endpoint: server.URL}}
	result := manager.Execute(ctx, call, resolved, contextinfo.Actor{Role: contextinfo.RoleSuperadmin})
	if result.Error != nil {
		t.Fatal(result.Error)
	}
	if string(received.Arguments) != string(args) {
		t.Fatalf("external arguments = %s", received.Arguments)
	}
}
