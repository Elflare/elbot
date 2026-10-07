package responses

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"elbot/internal/events"
	"elbot/internal/llm/httpclient"
)

func (e *APIError) LogDiagnostic() events.LogDiagnostic {
	return events.LogDiagnostic{Kind: e.EventType, Detail: e.Detail}
}

func (e *APIError) Error() string {
	message := errorSummary(e.Message, 256)
	if message == "" {
		message = "上游返回错误，但未提供可识别的错误说明"
	}
	if e.StatusCode != 0 {
		return fmt.Sprintf("HTTP %d: %s", e.StatusCode, message)
	}
	prefix := "response error"
	if e.EventType == "response.incomplete" {
		prefix = e.EventType
	}
	if code := errorSummary(e.Code, 256); code != "" {
		prefix += " " + code
	}
	return prefix + ": " + message
}

func streamAPIError(event Event) *APIError {
	apiErr := &APIError{}
	detail := event.Raw
	if event.Type == "error" {
		if strings.TrimSpace(event.Code) != "" || strings.TrimSpace(event.Message) != "" {
			apiErr.Code, apiErr.Message, apiErr.Param = event.Code, event.Message, event.Param
		} else {
			var envelope struct {
				Error json.RawMessage `json:"error"`
			}
			if json.Unmarshal(event.Raw, &envelope) == nil {
				var nested APIError
				if json.Unmarshal(envelope.Error, &nested) == nil {
					*apiErr = nested
				}
			}
		}
	} else {
		// Retain unknown error fields, but never the response's output or prompt.
		var failure struct {
			Error             json.RawMessage `json:"error,omitempty"`
			IncompleteDetails json.RawMessage `json:"incomplete_details,omitempty"`
		}
		if event.Response != nil {
			_ = json.Unmarshal(event.Response.Raw, &failure)
			if event.Response.Error != nil {
				*apiErr = *event.Response.Error
			} else if event.Response.IncompleteDetails != nil {
				apiErr.Message = event.Response.IncompleteDetails.Reason
			}
		}
		detail, _ = json.Marshal(failure)
	}
	apiErr.EventType = event.Type
	apiErr.Detail = errorDetail(detail)
	return apiErr
}

const maxErrorDetailBytes = 8 * 1024

// Free-form error strings may echo credentials. Keep the explanation preceding
// the first credential/base64 marker and discard the rest of that string.
var errorSecretMarker = regexp.MustCompile(`(?i)(?:\b(?:authorization|proxy[-_]?authorization|(?:x[-_])?api[-_]?key|access[-_]?token|refresh[-_]?token|id[-_]?token|token|password|client[-_]?secret|secret|cookie|set[-_]?cookie)\b["']?\s*[:=]\s*|\bbearer\s+|;base64,|base64://)`)

func redactErrorText(text string) string {
	if match := errorSecretMarker.FindStringIndex(text); match != nil {
		return text[:match[1]] + "[redacted]"
	}
	return text
}

func errorSummary(text string, limit int) string {
	return limitErrorText(strings.Join(strings.Fields(redactErrorText(text)), " "), limit)
}

func limitErrorText(text string, limit int) string {
	const suffix = "...[truncated]"
	if len(text) <= limit {
		return text
	}
	end := limit - len(suffix)
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end] + suffix
}

func errorDetail(raw json.RawMessage) string {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return "unavailable error details"
	}
	safe, _ := json.Marshal(redactErrorValue(value))
	return limitErrorText(string(safe), maxErrorDetailBytes)
}

func redactErrorValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		for key, field := range value {
			normalized := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(key))
			switch normalized {
			case "authorization", "proxyauthorization", "apikey", "xapikey", "token", "accesstoken", "refreshtoken", "idtoken", "password", "secret", "clientsecret", "cookie", "setcookie",
				"input", "output", "instructions", "encryptedcontent", "imageurl", "imagedata", "filedata", "audio", "base64":
				value[key] = "[redacted]"
			default:
				value[key] = redactErrorValue(field)
			}
		}
		return value
	case []any:
		for i, field := range value {
			value[i] = redactErrorValue(field)
		}
		return value
	case string:
		return redactErrorText(value)
	default:
		return value
	}
}

// Only explicit, structured chain errors qualify. Free-form messages, transport
// errors and unrelated invalid requests never trigger a context replay.
func PreviousResponseUnavailable(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	if apiErr.StatusCode != 0 && apiErr.StatusCode != http.StatusBadRequest && apiErr.StatusCode != http.StatusNotFound {
		return false
	}
	switch apiErr.Code {
	case "previous_response_not_found", "previous_response_id_not_found", "invalid_previous_response_id":
		return apiErr.Param == "" || apiErr.Param == "previous_response_id"
	case "response_not_found", "not_found":
		return apiErr.Param == "previous_response_id"
	}
	return false
}

func parseError(resp *http.Response) error {
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8193))
	if err != nil {
		return fmt.Errorf("HTTP %d (failed to read body: %w)", resp.StatusCode, err)
	}
	var apiErr struct {
		Error APIError `json:"error"`
	}
	if err := json.Unmarshal(body, &apiErr); err == nil && apiErr.Error.Message != "" {
		apiErr.Error.StatusCode = resp.StatusCode
		return &apiErr.Error
	}
	if strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/html") || httpclient.LooksLikeHTML(body) {
		return fmt.Errorf("HTTP %d: 上游返回 HTML 而不是 API 响应，请检查 API Base URL、代理或网关", resp.StatusCode)
	}
	return fmt.Errorf("HTTP %d: %s", resp.StatusCode, httpclient.SafeSummary(body))
}
