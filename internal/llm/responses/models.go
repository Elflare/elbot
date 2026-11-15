package responses

import (
	"context"
	"elbot/internal/llm"
	"elbot/internal/llm/share"
	"fmt"
	"net/http"
)

// ListModels fetches available model IDs from the /models endpoint.
func (a *Client) ListModels(ctx context.Context) ([]string, error) {
	result, err := a.ListModelMetadata(ctx)
	if err != nil {
		return nil, err
	}

	models := make([]string, len(result))
	for i, m := range result {
		models[i] = m.ID
	}
	return models, nil
}

func (a *Client) ListModelMetadata(ctx context.Context) ([]llm.ModelMetadata, error) {
	return llm.Retry(ctx, a, a.fetchModelsOnce, a.baseURL+"/models")
}

func (a *Client) fetchModelsOnce(ctx context.Context, url string) ([]llm.ModelMetadata, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+a.apiKey)
	resp, err := a.transport.Do(httpReq, parseError)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	models, err := share.DecodeModels(resp.Body)
	if err != nil {
		return nil, llm.MapTransportError(fmt.Errorf("decode models response: %w", err))
	}
	return models, nil
}
