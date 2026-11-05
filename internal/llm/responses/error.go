package responses

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"elbot/internal/llm"
)

func (e *APIError) Error() string { return e.Message }

func streamAPIError(event Event) error {
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
	if apiErr.Message == "" {
		apiErr.Message = string(detail)
	}
	// Preserve existing chain recovery; other stream errors remain terminal.
	// TODO: normalize other stream errors when retry output handling is implemented.
	failure := llm.MapProviderError(0, apiErr.Code, apiErr.Type, apiErr.Param, apiErr)
	if failure.Kind == llm.ErrorPreviousResponseUnavailable {
		return failure
	}
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

func limitErrorText(text string, limit int) string {
	const suffix = "…"
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

func parseError(resp *http.Response) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return llm.MapProviderError(resp.StatusCode, "", "", "", err)
	}
	var apiErr struct {
		Error APIError `json:"error"`
	}
	message := string(body)
	if err := json.Unmarshal(body, &apiErr); err == nil && apiErr.Error.Message != "" {
		message = apiErr.Error.Message
	}
	apiErr.Error.Message, apiErr.Error.StatusCode = message, resp.StatusCode
	return llm.MapProviderError(resp.StatusCode, apiErr.Error.Code, apiErr.Error.Type, apiErr.Error.Param, &apiErr.Error)
}
