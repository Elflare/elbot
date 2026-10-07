package responses

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"elbot/internal/llm"
)

func TestStreamErrorMessages(t *testing.T) {
	for _, tc := range []struct {
		name, raw, code, param, message string
	}{
		{"top level", `{"type":"error","code":"server_error","message":"upstream failed","param":"model"}`, "server_error", "model", "upstream failed"},
		{"nested", `{"type":"error","error":{"code":"server_error","message":"nested failure","param":"model"}}`, "server_error", "model", "nested failure"},
		{"prefer top level", `{"type":"error","code":"top","message":"top failure","error":{"code":"nested","message":"nested failure","param":"model"}}`, "top", "", "top failure"},
		{"top code only", `{"type":"error","code":"top","error":{"code":"nested","message":"nested failure","param":"model"}}`, "top", "", "上游返回错误，但未提供可识别的错误说明"},
		{"empty top level", `{"type":"error","code":" ","message":"\n","param":"ignored","error":{"code":"nested","message":"nested failure","param":"model"}}`, "nested", "model", "nested failure"},
		{"empty", `{"type":"error"}`, "", "", "上游返回错误，但未提供可识别的错误说明"},
		{"unknown fields", `{"type":"error","error":{"description":"unusual gateway error","upstream_status":504}}`, "", "", "上游返回错误，但未提供可识别的错误说明"},
		{"unstructured nested", `{"type":"error","error":"gateway failed"}`, "", "", "上游返回错误，但未提供可识别的错误说明"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			apiErr := streamErrorForTest(t, "error", tc.raw)
			err := error(apiErr)
			if apiErr.Code != tc.code || apiErr.Param != tc.param || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("code=%q param=%q error=%q", apiErr.Code, apiErr.Param, err.Error())
			}
			if strings.Contains(err.Error(), "error :") || strings.Contains(err.Error(), tc.raw) {
				t.Fatalf("invalid user-facing error: %s", err)
			}
			if apiErr.EventType != "error" || !json.Valid([]byte(apiErr.Detail)) {
				t.Fatalf("missing event diagnostics: %+v", apiErr)
			}
			if tc.name == "unknown fields" && !strings.Contains(apiErr.Detail, "unusual gateway error") {
				t.Fatalf("unknown fields lost: %s", apiErr.Detail)
			}
		})
	}
}

func streamErrorForTest(t *testing.T, eventType, raw string) *APIError {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		emit(w, eventType, raw)
	}))
	defer srv.Close()
	client := mustClient(t, srv.URL, nil, nil, RequestOptions{})
	_, err := client.GenerateText(context.Background(), llm.TextRequest{Model: "m", Input: "input"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected APIError, got %v", err)
	}
	return apiErr
}

func TestStreamFailureDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name, eventType, raw, want string
	}{
		{"failed", "response.failed", `{"type":"response.failed","response":{"id":"r","status":"failed","instructions":"private prompt","output":[{"type":"message","content":[{"type":"output_text","text":"private output"}]}],"error":{"code":"server_error","message":"generation failed","upstream_status":502}}}`, `"upstream_status":502`},
		{"incomplete", "response.incomplete", `{"type":"response.incomplete","response":{"id":"r","status":"incomplete","instructions":"private prompt","output":[],"incomplete_details":{"reason":"max_output_tokens","upstream_note":"limit reached"}}}`, `"upstream_note":"limit reached"`},
		{"missing response", "response.failed", `{"type":"response.failed"}`, `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			apiErr := streamErrorForTest(t, tc.eventType, tc.raw)
			if apiErr.EventType != tc.eventType || !strings.Contains(apiErr.Detail, tc.want) {
				t.Fatalf("event=%s detail=%s", apiErr.EventType, apiErr.Detail)
			}
			for _, private := range []string{"private prompt", "private output", `"instructions"`, `"output"`} {
				if strings.Contains(apiErr.Detail, private) {
					t.Fatalf("response content leaked: %s", apiErr.Detail)
				}
			}
			encoded, err := json.Marshal(apiErr)
			if err != nil || strings.Contains(string(encoded), "Detail") || strings.Contains(string(encoded), "EventType") || strings.Contains(string(encoded), "upstream_note") {
				t.Fatalf("diagnostics entered API JSON: %s %v", encoded, err)
			}
		})
	}
}

func TestStreamErrorRedactionAndLimits(t *testing.T) {
	raw := `{"type":"error","error":{"code":"server_error","message":"upstream failed Authorization: Bearer summary-secret","api_key":"key-secret","headers":{"Authorization":"auth-secret","X-Api-Key":"header-secret"},"details":[{"refresh_token":"refresh-secret","encrypted_content":"encrypted-secret","image_url":"image-secret","note":"upstream api_key=inline-secret"}],"image":"data:image/png;base64,image-base64-secret","request_id":9007199254740993}}`
	apiErr := streamErrorForTest(t, "error", raw)
	if strings.Contains(apiErr.Detail, "-secret") || strings.Contains(apiErr.Error(), "-secret") || !strings.Contains(apiErr.Detail, "[redacted]") {
		t.Fatalf("failed to redact: summary=%s detail=%s", apiErr.Error(), apiErr.Detail)
	}
	if !strings.Contains(apiErr.Detail, `"request_id":9007199254740993`) {
		t.Fatalf("numeric diagnostic changed: %s", apiErr.Detail)
	}
	longMessage := strings.Repeat("错误详情", 3000)
	raw = fmt.Sprintf(`{"type":"error","code":"long","message":%q}`, longMessage)
	apiErr = streamErrorForTest(t, "error", raw)
	if len(apiErr.Detail) > maxErrorDetailBytes || !utf8.ValidString(apiErr.Detail) || !strings.HasSuffix(apiErr.Detail, "...[truncated]") {
		t.Fatalf("invalid detail truncation: length=%d valid=%v", len(apiErr.Detail), utf8.ValidString(apiErr.Detail))
	}
	if len(apiErr.Error()) > 300 || !utf8.ValidString(apiErr.Error()) || !strings.HasSuffix(apiErr.Error(), "...[truncated]") {
		t.Fatalf("invalid summary truncation: %s", apiErr.Error())
	}
}

func TestStreamErrorsPreserveStructuredRecovery(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		want      bool
	}{
		{"nested chain error", `{"type":"error","error":{"code":"previous_response_not_found","param":"previous_response_id","message":"expired"}}`, true},
		{"unrelated top error", `{"type":"error","code":"server_error","message":"failed","error":{"code":"previous_response_not_found","param":"previous_response_id","message":"expired"}}`, false},
		{"do not mix params", `{"type":"error","code":"not_found","message":"missing","error":{"param":"previous_response_id"}}`, false},
		{"message only", `{"type":"error","message":"previous_response_not_found"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := fmt.Errorf("wrapped: %w", streamErrorForTest(t, "error", tc.raw))
			if PreviousResponseUnavailable(err) != tc.want {
				t.Fatalf("recovery=%v want=%v: %v", PreviousResponseUnavailable(err), tc.want, err)
			}
		})
	}
}
