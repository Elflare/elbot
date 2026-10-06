package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/storage"
)

func TestProductionRuntimeRunsBothHTTPProtocols(t *testing.T) {
	for _, mode := range []string{"chat", "response"} {
		t.Run(mode, func(t *testing.T) {
			req, platform, _ := runtimeAssemblyFixture(t)
			path := filepath.Join(filepath.Dir(req.Foundation.Config.ConfigPath), "read-me.txt")
			if err := os.WriteFile(path, []byte("assembled tool content"), 0600); err != nil {
				t.Fatal(err)
			}
			arguments, _ := json.Marshal(map[string]string{"path": path})
			var mu sync.Mutex
			var requests []map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/models" {
					fmt.Fprint(w, `{"data":[{"id":"first"}]}`)
					return
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				mu.Lock()
				index := len(requests)
				requests = append(requests, body)
				mu.Unlock()
				w.Header().Set("Content-Type", "text/event-stream")
				if mode == "chat" {
					if r.URL.Path != "/chat/completions" {
						t.Errorf("path=%s", r.URL.Path)
					}
					delta, finish := map[string]any{"content": "assembled HTTP reply"}, "stop"
					if index == 0 {
						delta, finish = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "read", "type": "function", "function": map[string]any{"name": "read_file", "arguments": string(arguments)}}}}, "tool_calls"
					}
					raw, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": delta, "finish_reason": finish}}})
					fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", raw)
				} else {
					if r.URL.Path != "/responses" {
						t.Errorf("path=%s", r.URL.Path)
					}
					item := map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "assembled HTTP reply"}}}
					if index == 0 {
						item = map[string]any{"type": "function_call", "call_id": "read", "name": "read_file", "arguments": string(arguments), "status": "completed"}
					}
					raw, _ := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{"id": fmt.Sprintf("r%d", index), "status": "completed", "output": []any{item}}})
					fmt.Fprintf(w, "event: response.completed\ndata: %s\n\n", raw)
				}
			}))
			t.Cleanup(server.Close)
			req.Foundation.Config.Providers = map[string]config.ProviderConfig{"test": {APIMode: mode, BaseURL: server.URL, Models: []string{"first"}}}
			models, err := (defaultModelFactory{}).Build(ModelRequest{Foundation: req.Foundation, Profiler: req.Profiler})
			if err != nil {
				t.Fatal(err)
			}
			req.Models = models
			runtime, err := (defaultRuntimeFactory{}).Build(t.Context(), req)
			t.Cleanup(func() { closeAssembledRuntime(t, runtime) })
			if err != nil {
				t.Fatal(err)
			}
			if _, err := (defaultIntegrationFactory{}).Attach(t.Context(), IntegrationRequest{Foundation: req.Foundation, Runtime: runtime, Platforms: req.Platforms, Mode: RunModeCLIOnly, Profiler: req.Profiler}); err != nil {
				t.Fatal(err)
			}
			ctx := contextinfo.WithActor(context.Background(), contextinfo.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: contextinfo.RoleSuperadmin})
			if err := runtime.Handler.HandleMessage(ctx, "@tool:read_file read the file"); err != nil {
				t.Fatal(err)
			}
			if err := runtime.Handler.HandleMessage(ctx, "continue"); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			captured := append([]map[string]any(nil), requests...)
			mu.Unlock()
			if len(captured) != 3 {
				t.Fatalf("requests=%+v", captured)
			}
			raw, _ := json.Marshal(captured[1])
			if !strings.Contains(string(raw), "assembled tool content") || !strings.Contains(platform.text(), "assembled HTTP reply") {
				t.Fatalf("tool/reply wiring: request=%s output=%s", raw, platform.text())
			}
			rows, err := req.Foundation.Store.Sessions().List(ctx, storage.ListSessionsRequest{IncludeAllPlatforms: true, Limit: 10})
			if err != nil || len(rows) != 1 {
				t.Fatalf("sessions=%+v %v", rows, err)
			}
			messages, err := req.Foundation.Store.Messages().ListBySession(ctx, rows[0].ID)
			if err != nil || len(messages) != 6 || messages[1].Role != storage.RoleAssistant || messages[2].Role != storage.RoleTool {
				t.Fatalf("history=%+v %v", messages, err)
			}
			resultID, err := storage.ToolResultMessageID(messages[1])
			if err != nil || resultID != messages[2].ID {
				t.Fatalf("tool pair=%+v %v", messages, err)
			}
			if mode == "response" && captured[2]["previous_response_id"] != "r1" {
				t.Fatalf("native continuation=%+v", captured[2])
			}
		})
	}
}
