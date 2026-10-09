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
	// PromptTokens is parsed from input aliases; nil means no valid count was reported.
	PromptTokens *int64
	// CompletionTokens is parsed from output aliases; an explicit zero is retained.
	CompletionTokens *int64
	// TotalTokens is only the reported total; missing or invalid totals remain nil.
	TotalTokens *int64
	// CacheHitTokens is parsed from cache aliases, including nested details; nil is unknown.
	CacheHitTokens *int64
}

func (u *openAIUsage) UnmarshalJSON(data []byte) error {
	*u = openAIUsage{}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		// Invalid usage does not invalidate an otherwise usable model response.
		return nil
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
	if u.CacheHitTokens == nil {
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

func firstJSONInt(raw map[string]json.RawMessage, names ...string) *int64 {
	for _, name := range names {
		if value := jsonInt(raw[name]); value != nil {
			return value
		}
	}
	return nil
}

func firstNestedJSONInt(raw map[string]json.RawMessage, paths ...[]string) *int64 {
	for _, path := range paths {
		if len(path) == 0 {
			continue
		}
		current := raw[path[0]]
		for _, key := range path[1:] {
			var object map[string]json.RawMessage
			if err := json.Unmarshal(current, &object); err != nil {
				current = nil
				break
			}
			current = object[key]
		}
		if value := jsonInt(current); value != nil {
			return value
		}
	}
	return nil
}

func jsonInt(value json.RawMessage) *int64 {
	var i *int64
	if err := json.Unmarshal(value, &i); err != nil || i == nil || *i < 0 {
		return nil
	}
	return i
}
