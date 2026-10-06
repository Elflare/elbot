package chatcompletions

import (
	"elbot/internal/llm/httpclient"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type openAIError struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

func parseError(resp *http.Response) error {
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8192+1))
	if err != nil {
		return fmt.Errorf("HTTP %d (failed to read body: %w)", resp.StatusCode, err)
	}

	var apiErr openAIError
	if err := json.Unmarshal(body, &apiErr); err == nil && apiErr.Error.Message != "" {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, httpclient.SafeSummary([]byte(apiErr.Error.Message)))
	}
	if strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/html") || httpclient.LooksLikeHTML(body) {
		return fmt.Errorf("HTTP %d: 上游返回 HTML 而不是 API 响应，请检查 API Base URL、代理或网关", resp.StatusCode)
	}
	return fmt.Errorf("HTTP %d: %s", resp.StatusCode, httpclient.SafeSummary(body))
}
