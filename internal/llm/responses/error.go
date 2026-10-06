package responses

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"elbot/internal/llm/httpclient"
)

func (e *APIError) Error() string {
	if e.StatusCode != 0 {
		return fmt.Sprintf("HTTP %d: %s", e.StatusCode, httpclient.SafeSummary([]byte(e.Message)))
	}
	return fmt.Sprintf("response error %s: %s", e.Code, httpclient.SafeSummary([]byte(e.Message)))
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
