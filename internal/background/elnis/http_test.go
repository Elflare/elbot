package elnis

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"elbot/internal/config"
	"elbot/internal/delivery"
	"elbot/internal/events"
)

func TestNativeCallsHTTPExecutionAndTerminalState(t *testing.T) {
	for _, tt := range []struct {
		name    string
		content string
		failure error
	}{
		{name: "message", content: " body "},
		{name: "calls-only"},
		{name: "failure", content: "body", failure: errors.New("native failure")},
		{name: "canceled", content: "body", failure: context.Canceled},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var operations []string
			service, cleanup := newTestService(t, func(_ context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
				operations = append(operations, "send")
				if target.Platform != "qqonebot" || target.PrivateUserID != "admin" || target.Superadmins || len(outputs) != 1 || outputs[0].Text != "title\nbody" {
					t.Fatalf("direct output: target=%+v outputs=%+v", target, outputs)
				}
				return delivery.Receipt{}, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
			})
			defer cleanup()
			params := map[string]any{"values": []any{float64(42), true, nil}, "nested": map[string]any{"id": "007"}}
			service.callPlatformAPI = func(_ context.Context, platform, api string, args map[string]any) (json.RawMessage, error) {
				operations = append(operations, api)
				if api == "first" && (platform != "qqonebot" || !reflect.DeepEqual(args, params)) {
					t.Fatalf("fallback platform or parameters changed: %q %#v", platform, args)
				}
				if api == "second" {
					if platform != "telegram" || args != nil {
						t.Fatalf("explicit call changed: %q %#v", platform, args)
					}
					return nil, tt.failure
				}
				return json.RawMessage(`{"native":true}`), nil
			}
			runtime := startTestRuntime(t, config.Default().Elnis.HTTP, service)
			request := validHTTPEvent(tt.name)
			request["mode"], request["content"] = ModeDirect, tt.content
			if tt.content != "" {
				request["title"] = " title "
			}
			request["targets"] = []Target{{Platform: "qqonebot", Type: "private", ID: "admin"}}
			request["calls"] = []Call{{API: "first", Params: params}, {Platform: "telegram", API: "second"}, {API: "third"}}
			body, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			response := serveElnisJSON(runtime, body)
			if response.Code != http.StatusAccepted {
				t.Fatalf("HTTP status=%d body=%s", response.Code, response.Body.String())
			}
			var result Response
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			wantStatus := StatusCompleted
			wantOperations := []string{"first", "second", "third"}
			if tt.failure != nil {
				wantStatus = StatusFailed
				wantOperations = []string{"first", "second"}
				if result.Error != tt.failure.Error() {
					t.Fatalf("native error changed: %q", result.Error)
				}
			} else if tt.content != "" {
				wantOperations = append(wantOperations, "send")
			}
			if !result.Accepted || result.Status != wantStatus || !reflect.DeepEqual(operations, wantOperations) {
				t.Fatalf("result=%+v operations=%v, want %s %v", result, operations, wantStatus, wantOperations)
			}
			stored, err := testRepo(service).snapshotByKey(t.Context(), "watcher", "http-test", tt.name)
			if err != nil || stored.Status != wantStatus {
				t.Fatalf("terminal state: %+v %v", stored, err)
			}
		})
	}
}

func TestNewRuntimeConfiguresHTTPTimeouts(t *testing.T) {
	service, cleanup := newTestService(t, nil)
	defer cleanup()

	defaults := startTestRuntime(t, config.Default().Elnis.HTTP, service).server
	if defaults.ReadHeaderTimeout != 5*time.Second || defaults.ReadTimeout != 30*time.Second || defaults.WriteTimeout != 300*time.Second || defaults.IdleTimeout != 60*time.Second {
		t.Fatalf("default timeouts = header:%s read:%s write:%s idle:%s", defaults.ReadHeaderTimeout, defaults.ReadTimeout, defaults.WriteTimeout, defaults.IdleTimeout)
	}

	cfg := config.Default().Elnis.HTTP
	cfg.ReadHeaderTimeoutSeconds, cfg.ReadTimeoutSeconds = 7, 11
	cfg.WriteTimeoutSeconds, cfg.IdleTimeoutSeconds = 13, 17
	configured := startTestRuntime(t, cfg, service).server
	if configured.ReadHeaderTimeout != 7*time.Second || configured.ReadTimeout != 11*time.Second || configured.WriteTimeout != 13*time.Second || configured.IdleTimeout != 17*time.Second {
		t.Fatalf("configured timeouts = header:%s read:%s write:%s idle:%s", configured.ReadHeaderTimeout, configured.ReadTimeout, configured.WriteTimeout, configured.IdleTimeout)
	}
}

func TestElnisHTTPAcceptsSingleJSONAndIgnoresUnknownFields(t *testing.T) {
	service, cleanup := newTestService(t, nil)
	defer cleanup()
	runtime := startTestRuntime(t, config.Default().Elnis.HTTP, service)

	request := validHTTPEvent("unknown-field")
	request["future_field"] = map[string]any{"enabled": true}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	body = append(body, '\n', ' ', '\t')

	response := serveElnisJSON(runtime, body)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestElnisHTTPRejectsTrailingJSON(t *testing.T) {
	tests := []struct {
		name     string
		trailing string
	}{
		{name: "object", trailing: ` {}`},
		{name: "array", trailing: ` []`},
		{name: "scalar", trailing: ` true`},
		{name: "invalid", trailing: ` not-json`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service, cleanup := newTestService(t, nil)
			defer cleanup()
			runtime := startTestRuntime(t, config.Default().Elnis.HTTP, service)
			body, err := json.Marshal(validHTTPEvent("trailing-" + tt.name))
			if err != nil {
				t.Fatal(err)
			}
			body = append(body, tt.trailing...)

			response := serveElnisJSON(runtime, body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			if !strings.Contains(response.Body.String(), "invalid json") {
				t.Fatalf("response body = %s", response.Body.String())
			}
		})
	}
}

func TestElnisHTTPPreservesRoutesAuthAndBodyLimit(t *testing.T) {
	service, cleanup := newTestService(t, nil)
	defer cleanup()
	body, err := json.Marshal(validHTTPEvent("http-boundaries"))
	if err != nil {
		t.Fatal(err)
	}

	runtime := startTestRuntime(t, config.Default().Elnis.HTTP, service)
	response := serveElnisRequest(runtime, "/elvena/v3/events", body, "secret")
	if response.Code != http.StatusAccepted {
		t.Fatalf("v3 status = %d, body = %s", response.Code, response.Body.String())
	}

	response = serveElnisRequest(runtime, "/elvena/v2/events", body, "")
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, body = %s", response.Code, response.Body.String())
	}

	cfg := config.Default().Elnis.HTTP
	cfg.MaxBodyBytes = 16
	limited := startTestRuntime(t, cfg, service)
	response = serveElnisRequest(limited, "/elvena/v2/events", body, "secret")
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "request body too large") {
		t.Fatalf("oversized status = %d, body = %s", response.Code, response.Body.String())
	}
}

func validHTTPEvent(id string) map[string]any {
	return map[string]any{
		"version": "elvena.v2",
		"elwisp":  map[string]any{"name": "watcher", "tags": []string{"test"}},
		"source":  "http-test",
		"id":      id,
		"mode":    "record",
		"format":  "text",
		"content": "hello",
		"targets": []map[string]any{{"platform": "cli"}},
	}
}

func serveElnisJSON(runtime *Runtime, body []byte) *httptest.ResponseRecorder {
	return serveElnisRequest(runtime, "/elvena/v2/events", body, "secret")
}

func serveElnisRequest(runtime *Runtime, path string, body []byte, token string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	runtime.server.Handler.ServeHTTP(response, request)
	return response
}
