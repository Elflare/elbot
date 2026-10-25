package execution

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"elbot/internal/events"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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
	manager := newTestManager(nil, nil)
	args, _ := json.Marshal(map[string]any{"source": mediaID, "media_inputs": []map[string]string{{"media": mediaID}}})
	call := llm.ToolCallRequest{ID: "call", Name: "external", Arguments: string(args)}
	resolved := resolvedTool{ResultStatus: events.ResultSucceeded, Source: toolshare.SourceKindELwisp, Discovered: &toolshare.DiscoveredTool{Name: "external", Endpoint: server.URL}}
	result := manager.executePrepared(ctx, call, resolved, nil, nil)
	if result.Error != nil {
		t.Fatal(result.Error)
	}
	if string(received.Arguments) != string(args) {
		t.Fatalf("external arguments = %s", received.Arguments)
	}
}
