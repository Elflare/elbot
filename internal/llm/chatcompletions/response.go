package chatcompletions

import (
	"elbot/internal/llm"
	"encoding/json"
)

// Chunk is a single chunk of a streaming response.
type Chunk struct {
	DeltaContent          string
	DeltaReasoningContent string
	ToolCallDeltas        []ToolCallDelta
	FinishReason          string
	Usage                 *llm.Usage
	Error                 error
}

// ToolCallDelta is an incremental tool call fragment from a stream.
type ToolCallDelta struct {
	Index int
	ID    string
	Name  string
	Args  string // incremental JSON fragment
}

type openAIStreamChunk struct {
	Choices []openAIChoice `json:"choices"`
	Usage   *openAIUsage   `json:"usage"`
}

type openAIChoice struct {
	Index        int         `json:"index"`
	Delta        openAIDelta `json:"delta"`
	FinishReason *string     `json:"finish_reason"`
}

type openAIDelta struct {
	Content          string                `json:"content"`
	ReasoningContent string                `json:"reasoning_content"`
	ToolCalls        []openAIToolCallDelta `json:"tool_calls"`
}

type openAIToolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type openAIUsage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CacheHitTokens   int
}

func (u *openAIUsage) UnmarshalJSON(data []byte) error {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	u.PromptTokens = firstJSONInt(raw, "prompt_tokens", "input_tokens")
	u.CompletionTokens = firstJSONInt(raw, "completion_tokens", "output_tokens")
	u.TotalTokens = firstJSONInt(raw, "total_tokens")
	u.CacheHitTokens = firstJSONInt(raw,
		"prompt_cache_hit_tokens",
		"cache_hit_tokens",
		"cached_tokens",
		"input_cache_hit_tokens",
		"prompt_cache_read_tokens",
		"cache_read_input_tokens",
	)
	if u.CacheHitTokens == 0 {
		u.CacheHitTokens = firstNestedJSONInt(raw,
			[]string{"prompt_tokens_details", "cached_tokens"},
			[]string{"prompt_tokens_details", "cache_hit_tokens"},
			[]string{"input_tokens_details", "cached_tokens"},
			[]string{"input_tokens_details", "cache_hit_tokens"},
		)
	}
	return nil
}
func toUsage(u *openAIUsage) *llm.Usage {
	return &llm.Usage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
		CacheHitTokens:   u.CacheHitTokens,
	}
}

func firstJSONInt(raw map[string]any, names ...string) int {
	for _, name := range names {
		if value := jsonInt(raw[name]); value > 0 {
			return value
		}
	}
	return 0
}

func firstNestedJSONInt(raw map[string]any, paths ...[]string) int {
	for _, path := range paths {
		var current any = raw
		for _, key := range path {
			object, ok := current.(map[string]any)
			if !ok {
				current = nil
				break
			}
			current = object[key]
		}
		if value := jsonInt(current); value > 0 {
			return value
		}
	}
	return 0
}

func jsonInt(value any) int {
	switch v := value.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case json.Number:
		i, _ := v.Int64()
		return int(i)
	default:
		return 0
	}
}
