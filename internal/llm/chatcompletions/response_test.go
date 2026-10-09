package chatcompletions

import (
	"encoding/json"
	"testing"
)

func TestUsagePreservesKnownAndUnknownCounts(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want [4]*int64
	}{
		{name: "missing", body: `{}`},
		{name: "null", body: `null`},
		{name: "invalid object", body: `[]`},
		{name: "invalid scalar", body: `"broken"`},
		{name: "partial", body: `{"prompt_tokens":3}`, want: [4]*int64{testCount(3)}},
		{name: "zero wins", body: `{"prompt_tokens":0,"input_tokens":8,"prompt_cache_hit_tokens":0,"input_tokens_details":{"cached_tokens":9}}`, want: [4]*int64{testCount(0), nil, nil, testCount(0)}},
		{name: "invalid aliases fall through", body: `{"prompt_tokens":null,"input_tokens":8,"completion_tokens":-1,"output_tokens":2,"prompt_cache_hit_tokens":"bad","cache_hit_tokens":4}`, want: [4]*int64{testCount(8), testCount(2), nil, testCount(4)}},
		{name: "invalid nested aliases fall through", body: `{"prompt_tokens_details":{"cached_tokens":-1,"cache_hit_tokens":null},"input_tokens_details":{"cached_tokens":0,"cache_hit_tokens":9}}`, want: [4]*int64{nil, nil, nil, testCount(0)}},
		{name: "malformed details fall through", body: `{"prompt_tokens_details":false,"input_tokens_details":{"cached_tokens":7}}`, want: [4]*int64{nil, nil, nil, testCount(7)}},
		{name: "invalid counts", body: `{"prompt_tokens":-1,"completion_tokens":1.5,"total_tokens":9223372036854775808,"cached_tokens":true}`},
		{name: "exact large integers", body: `{"prompt_tokens":9007199254740993,"total_tokens":9223372036854775807}`, want: [4]*int64{testCount(9007199254740993), nil, testCount(9223372036854775807)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var usage openAIUsage
			if err := json.Unmarshal([]byte(tc.body), &usage); err != nil {
				t.Fatalf("usage must not fail the response: %v", err)
			}
			got := toUsage(&usage)
			for i, value := range [4]*int64{got.PromptTokens, got.CompletionTokens, got.TotalTokens, got.CacheHitTokens} {
				if (value == nil) != (tc.want[i] == nil) || value != nil && *value != *tc.want[i] {
					t.Fatalf("count %d: got %v, want %v", i, value, tc.want[i])
				}
			}
			if err := json.Unmarshal([]byte(`{}`), &usage); err != nil {
				t.Fatal(err)
			}
			if usage.PromptTokens != nil || usage.CompletionTokens != nil || usage.TotalTokens != nil || usage.CacheHitTokens != nil {
				t.Fatal("decoding reused usage retained stale counts")
			}
		})
	}
}

func testCount(value int64) *int64 { return &value }
