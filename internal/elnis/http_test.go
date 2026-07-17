package elnis

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"elbot/internal/config"
)

func TestNewRuntimeConfiguresHTTPTimeouts(t *testing.T) {
	service, cleanup := newTestService(t, nil)
	defer cleanup()

	defaults := NewRuntime(config.ElnisHTTPConfig{}, service).server
	if defaults.ReadHeaderTimeout != 5*time.Second || defaults.ReadTimeout != 30*time.Second || defaults.WriteTimeout != 300*time.Second || defaults.IdleTimeout != 60*time.Second {
		t.Fatalf("default timeouts = header:%s read:%s write:%s idle:%s", defaults.ReadHeaderTimeout, defaults.ReadTimeout, defaults.WriteTimeout, defaults.IdleTimeout)
	}

	configured := NewRuntime(config.ElnisHTTPConfig{
		ReadHeaderTimeoutSeconds: 7,
		ReadTimeoutSeconds:       11,
		WriteTimeoutSeconds:      13,
		IdleTimeoutSeconds:       17,
	}, service).server
	if configured.ReadHeaderTimeout != 7*time.Second || configured.ReadTimeout != 11*time.Second || configured.WriteTimeout != 13*time.Second || configured.IdleTimeout != 17*time.Second {
		t.Fatalf("configured timeouts = header:%s read:%s write:%s idle:%s", configured.ReadHeaderTimeout, configured.ReadTimeout, configured.WriteTimeout, configured.IdleTimeout)
	}
}

func TestElnisHTTPAcceptsSingleJSONAndIgnoresUnknownFields(t *testing.T) {
	service, cleanup := newTestService(t, nil)
	defer cleanup()
	runtime := NewRuntime(config.ElnisHTTPConfig{}, service)

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
			runtime := NewRuntime(config.ElnisHTTPConfig{}, service)
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

	runtime := NewRuntime(config.ElnisHTTPConfig{}, service)
	response := serveElnisRequest(runtime, "/elvena/v3/events", body, "secret")
	if response.Code != http.StatusAccepted {
		t.Fatalf("v3 status = %d, body = %s", response.Code, response.Body.String())
	}

	response = serveElnisRequest(runtime, "/elvena/v2/events", body, "")
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, body = %s", response.Code, response.Body.String())
	}

	limited := NewRuntime(config.ElnisHTTPConfig{MaxBodyBytes: 16}, service)
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
