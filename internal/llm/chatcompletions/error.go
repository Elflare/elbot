package chatcompletions

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"elbot/internal/llm"
)

type openAIError struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`  // Provider code consumed by the boundary mapper.
		Param   string `json:"param"` // Request field associated with the provider error.
	} `json:"error"`
}

func parseError(resp *http.Response) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return llm.MapProviderError(resp.StatusCode, "", "", "", err)
	}

	var apiErr openAIError
	message := string(body)
	if err := json.Unmarshal(body, &apiErr); err == nil && apiErr.Error.Message != "" {
		message = apiErr.Error.Message
	}
	return llm.MapProviderError(resp.StatusCode, apiErr.Error.Code, apiErr.Error.Type, apiErr.Error.Param, errors.New(message))
}
