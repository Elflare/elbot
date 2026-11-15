package share

import (
	"encoding/json"
	"io"
)

type ModelMetadata struct {
	ID            string // ID is the provider's model identifier.
	ContextWindow int    // ContextWindow is the reported token limit; zero means unknown.
}

// DecodeModels reads the shared model catalog format and its context-window aliases.
func DecodeModels(reader io.Reader) ([]ModelMetadata, error) {
	var result struct {
		Data []struct {
			ID                 string `json:"id"`
			ContextWindowValue int    `json:"context_window"`
			MaxContextLength   int    `json:"max_context_length"`
			MaxInputTokens     int    `json:"max_input_tokens"`
			MaxTokens          int    `json:"max_tokens"`
		} `json:"data"`
	}
	if err := json.NewDecoder(reader).Decode(&result); err != nil {
		return nil, err
	}
	models := make([]ModelMetadata, len(result.Data))
	for i, m := range result.Data {
		models[i].ID = m.ID
		for _, value := range []int{m.ContextWindowValue, m.MaxContextLength, m.MaxInputTokens, m.MaxTokens} {
			if value > 0 {
				models[i].ContextWindow = value
				break
			}
		}
	}
	return models, nil
}
