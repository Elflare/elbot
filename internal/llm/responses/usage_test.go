package responses

import (
	"encoding/json"
	"testing"
)

func TestResponseUsagePreservesUnknownAndExactCounts(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want [4]*int64
	}{
		{"missing", `{}`, [4]*int64{}},
		{"invalid shape", `[]`, [4]*int64{}},
		{"partial", `{"input_tokens":5}`, [4]*int64{testUsageCount(5)}},
		{"zeros", `{"input_tokens":0,"output_tokens":0,"total_tokens":0,"input_tokens_details":{"cached_tokens":0}}`, [4]*int64{testUsageCount(0), testUsageCount(0), testUsageCount(0), testUsageCount(0)}},
		{"invalid counts", `{"input_tokens":-1,"output_tokens":1.5,"total_tokens":9223372036854775808,"input_tokens_details":{"cached_tokens":"bad"}}`, [4]*int64{}},
		{"large exact counts", `{"input_tokens":9007199254740993,"total_tokens":9223372036854775807}`, [4]*int64{testUsageCount(9007199254740993), nil, testUsageCount(9223372036854775807)}},
		{"null and valid", `{"input_tokens":null,"output_tokens":2,"input_tokens_details":{"cached_tokens":4}}`, [4]*int64{nil, testUsageCount(2), nil, testUsageCount(4)}},
		{"malformed details", `{"input_tokens":2,"input_tokens_details":false}`, [4]*int64{testUsageCount(2)}},
		{"top level cache must not leak", `{"cached_tokens":9,"input_tokens_details":{}}`, [4]*int64{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var response Response
			body := `{"id":"r","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"usable"}]}],"usage":` + tc.body + `}`
			if err := json.Unmarshal([]byte(body), &response); err != nil {
				t.Fatalf("invalid usage must not fail a usable response: %v", err)
			}
			if response.Text() != "usable" {
				t.Fatal("response text lost")
			}
			usage := response.TokenUsage()
			if usage == nil {
				t.Fatal("usage object must retain partial unknown fields")
			}
			for i, got := range [4]*int64{usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens, usage.CacheHitTokens} {
				if (got == nil) != (tc.want[i] == nil) || got != nil && *got != *tc.want[i] {
					t.Fatalf("count %d: got %v, want %v", i, got, tc.want[i])
				}
			}
			if err := json.Unmarshal([]byte(`{}`), response.Usage); err != nil {
				t.Fatal(err)
			}
			if response.Usage.InputTokens != nil || response.Usage.OutputTokens != nil || response.Usage.TotalTokens != nil || response.Usage.InputTokensDetails.CachedTokens != nil {
				t.Fatal("reused usage retained old counts")
			}
		})
	}
}

func testUsageCount(value int64) *int64 { return &value }
