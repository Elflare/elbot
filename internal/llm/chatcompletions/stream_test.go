package chatcompletions

import (
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
)

func TestChatStream_CompletedOutput(t *testing.T) {
	for _, tt := range []struct {
		name      string
		frames    []string
		calls     []ToolCallDelta
		text      string
		reasoning string
		finish    string
	}{
		{
			name: "last arguments arrive with finish",
			frames: []string{
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"lookup","arguments":"{\"name\":"}}]}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"alpha\"}"}}]},"finish_reason":"tool_calls"}]}`,
			},
			calls:  []ToolCallDelta{{Index: 0, ID: "call_1", Name: "lookup", Args: `{"name":"alpha"}`}},
			finish: "tool_calls",
		},
		{
			name: "interleaved calls retain index order",
			frames: []string{
				`{"choices":[{"delta":{"tool_calls":[{"index":2,"id":"call_2","function":{"name":"lookup","arguments":"{\"name\":"}}]}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_0","function":{"name":"lookup","arguments":"{\"name\":"}},{"index":2,"function":{"arguments":"\"beta\"}"}}]}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"alpha\"}"}}]},"finish_reason":"tool_calls"}]}`,
			},
			calls: []ToolCallDelta{
				{Index: 0, ID: "call_0", Name: "lookup", Args: `{"name":"alpha"}`},
				{Index: 2, ID: "call_2", Name: "lookup", Args: `{"name":"beta"}`},
			},
			finish: "tool_calls",
		},
		{
			name: "text and reasoning finish without tools",
			frames: []string{
				`{"choices":[{"delta":{"content":"hello","reasoning_content":"think"}}]}`,
				`{"choices":[{"delta":{"content":" world","reasoning_content":" done"},"finish_reason":"stop"}]}`,
			},
			text:      "hello world",
			reasoning: "think done",
			finish:    "stop",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			frames := append(slices.Clone(tt.frames),
				`{"choices":[{"delta":{},"finish_reason":"`+tt.finish+`"}]}`,
				`{"choices":[{"delta":{"content":"late","reasoning_content":"late","tool_calls":[{"index":0,"id":"late","function":{"name":"lookup","arguments":"{}"}}]}}]}`,
				`{"choices":[],"usage":{"prompt_tokens":11,"completion_tokens":2,"total_tokens":13}}`,
				`{"choices":[{"delta":{"content":"late","reasoning_content":"late","tool_calls":[{"index":0,"function":{"arguments":"late"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":11,"completion_tokens":3,"total_tokens":14}}`,
				`[DONE]`,
			)
			var calls []ToolCallDelta
			var text, reasoning, finish string
			var totals []int64
			finishCount := 0
			for _, chunk := range readTestStream(t, frames) {
				if chunk.Error != nil {
					t.Fatal(chunk.Error)
				}
				calls = append(calls, chunk.ToolCallDeltas...)
				text += chunk.DeltaContent
				reasoning += chunk.DeltaReasoningContent
				if chunk.FinishReason != "" {
					finish, finishCount = chunk.FinishReason, finishCount+1
				}
				if chunk.Usage != nil && chunk.Usage.TotalTokens != nil {
					totals = append(totals, *chunk.Usage.TotalTokens)
				}
			}
			if !slices.Equal(calls, tt.calls) {
				t.Errorf("calls = %+v, want %+v", calls, tt.calls)
			}
			if text != tt.text || reasoning != tt.reasoning {
				t.Errorf("text/reasoning = %q/%q, want %q/%q", text, reasoning, tt.text, tt.reasoning)
			}
			if finish != tt.finish || finishCount != 1 {
				t.Errorf("finish = %q (%d times), want %q once", finish, finishCount, tt.finish)
			}
			if !slices.Equal(totals, []int64{13, 14}) {
				t.Errorf("usage totals = %v, want [13 14]", totals)
			}
		})
	}
}

func TestChatStream_MissingDoneAfterFinish(t *testing.T) {
	chunks := readTestStream(t, []string{
		`{"choices":[{"delta":{"content":"complete"},"finish_reason":"stop"}]}`,
		`{"choices":[],"usage":{"total_tokens":13}}`,
	})
	var gotErr error
	for _, chunk := range chunks {
		if chunk.Error != nil {
			gotErr = chunk.Error
		}
	}
	if gotErr == nil || !strings.Contains(gotErr.Error(), "unexpected EOF") {
		t.Fatalf("error = %v, want unexpected EOF", gotErr)
	}
}

func readTestStream(t *testing.T, frames []string) []Chunk {
	t.Helper()
	var body strings.Builder
	for _, frame := range frames {
		body.WriteString("data: " + frame + "\n\n")
	}
	adapter := mustNewWithOptions(t, "https://example.invalid", "test-key", nil, nil, RequestOptions{})
	adapter.transport = mustTransport(t, &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(body.String())),
			Request:    req,
		}, nil
	})})
	ch, err := adapter.Stream(t.Context(), Request{Model: "test"})
	if err != nil {
		t.Fatal(err)
	}
	var chunks []Chunk
	for chunk := range ch {
		chunks = append(chunks, chunk)
	}
	return chunks
}
