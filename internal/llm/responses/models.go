package responses

import (
	"context"
	"elbot/internal/llm"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// ListModels fetches available model IDs from the /models endpoint.
func (a *Client) ListModels(ctx context.Context) ([]string, error) {
	result, err := a.fetchModels(ctx)
	if err != nil {
		return nil, err
	}

	models := make([]string, len(result.Data))
	for i, m := range result.Data {
		models[i] = m.ID
	}
	return models, nil
}

func (a *Client) ListModelMetadata(ctx context.Context) ([]llm.ModelMetadata, error) {
	result, err := a.fetchModels(ctx)
	if err != nil {
		return nil, err
	}

	models := make([]llm.ModelMetadata, 0, len(result.Data))
	for _, m := range result.Data {
		models = append(models, llm.ModelMetadata{ID: m.ID, ContextWindow: m.ContextWindow()})
	}
	return models, nil
}

func (a *Client) fetchModels(ctx context.Context) (*modelList, error) {
	if err := a.validateBaseURL(); err != nil {
		return nil, err
	}
	url := strings.TrimRight(a.baseURL, "/") + "/models"

	resp, err := a.transport.Do(ctx, func(ctx context.Context) (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}
		httpReq.Header.Set("Authorization", "Bearer "+a.apiKey)
		return httpReq, nil
	}, parseError)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, parseError(resp)
	}

	var result modelList
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode models response: %w", err)
	}
	return &result, nil
}

type modelList struct {
	Data []modelMetadata `json:"data"`
}

type modelMetadata struct {
	ID                 string `json:"id"`
	ContextWindowValue int    `json:"context_window"`
	MaxContextLength   int    `json:"max_context_length"`
	MaxInputTokens     int    `json:"max_input_tokens"`
	MaxTokens          int    `json:"max_tokens"`
}

func (m modelMetadata) ContextWindow() int {
	for _, value := range []int{m.ContextWindowValue, m.MaxContextLength, m.MaxInputTokens, m.MaxTokens} {
		if value > 0 {
			return value
		}
	}
	return 0
}
