package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"elbot/internal/llm"
)

func TestChatStream_BasicContent(t *testing.T) {
	var capturedPath string
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		chunks := []string{
			`data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}`,
			`data: {"id":"2","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":null}]}`,
			`data: {"id":"3","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`,
			`data: [DONE]`,
		}
		for _, c := range chunks {
			io.WriteString(w, c+"\n\n")
			w.(http.Flusher).Flush()
		}
	}))
	defer srv.Close()

	adapter := New(srv.URL+"/", "test-key", nil)
	ch, err := adapter.ChatStream(context.Background(), llm.ChatRequest{
		Model: "test",
		Messages: []llm.LLMMessage{
			{Role: llm.RoleUser, Segments: llm.TextSegments("Hi")},
		},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	var content strings.Builder
	var lastUsage *llm.Usage
	var finishReason string
	for chunk := range ch {
		content.WriteString(chunk.DeltaContent)
		if chunk.Usage != nil {
			lastUsage = chunk.Usage
		}
		if chunk.FinishReason != "" {
			finishReason = chunk.FinishReason
		}
	}

	if content.String() != "Hello world" {
		t.Errorf("expected 'Hello world', got %q", content.String())
	}
	if finishReason != "stop" {
		t.Errorf("expected finish_reason 'stop', got %q", finishReason)
	}
	if lastUsage == nil {
		t.Fatal("expected usage in final chunk")
	}
	if capturedPath != "/chat/completions" {
		t.Errorf("expected endpoint path /chat/completions, got %q", capturedPath)
	}

	var requestBody map[string]any
	if err := json.Unmarshal(capturedBody, &requestBody); err != nil {
		t.Fatalf("unmarshal captured body: %v", err)
	}
	streamOptions, ok := requestBody["stream_options"].(map[string]any)
	if !ok {
		t.Fatalf("expected stream_options in request body, got %#v", requestBody["stream_options"])
	}
	if streamOptions["include_usage"] != true {
		t.Errorf("expected stream_options.include_usage=true, got %#v", streamOptions["include_usage"])
	}
}

func TestChatStream_DebugLogIncludesLatestMessageJSON(t *testing.T) {
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	var logs bytes.Buffer
	adapter := New(srv.URL, "secret-key", nil)
	adapter.SetLogger(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	ch, err := adapter.ChatStream(context.Background(), llm.ChatRequest{
		Model: "test",
		Messages: []llm.LLMMessage{
			{Role: llm.RoleUser, Segments: llm.TextSegments("旧消息")},
			{Role: llm.RoleAssistant, Segments: llm.TextSegments("旧回答")},
			{Role: llm.RoleUser, Segments: []llm.MessageSegment{
				{Type: llm.SegmentText, Text: "看图"},
				{Type: llm.SegmentImage, URL: "https://example.com/a.png"},
			}},
		},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	for range ch {
	}

	logText := logs.String()
	if !strings.Contains(logText, "latest_message_json=") || !strings.Contains(logText, "https://example.com/a.png") {
		t.Fatalf("debug log did not include latest message json: %s", logText)
	}
	if strings.Contains(logText, "body_json=") || strings.Contains(logText, "旧消息") || strings.Contains(logText, "旧回答") {
		t.Fatalf("debug log should not include full request history by default: %s", logText)
	}
	if !strings.Contains(logText, "message_count=3") || !strings.Contains(logText, "message_roles=user,assistant,user") || !strings.Contains(logText, "latest_message=user") || !strings.Contains(logText, "body_hash=") {
		t.Fatalf("debug log did not include request summary fields: %s", logText)
	}

	if strings.Contains(logText, "secret-key") || strings.Contains(logText, "Authorization") {
		t.Fatalf("debug log leaked credentials: %s", logText)
	}
	if len(capturedBody) == 0 {
		t.Fatal("server did not receive request body")
	}
	if !bytes.Contains(capturedBody, []byte("旧消息")) || !bytes.Contains(capturedBody, []byte("旧回答")) || !bytes.Contains(capturedBody, []byte("https://example.com/a.png")) {
		t.Fatalf("actual request body should keep full context, got: %s", string(capturedBody))
	}
}

func TestChatStreamLogsFirstSystemMessageOncePerSession(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	var logs bytes.Buffer
	adapter := New(srv.URL, "secret-key", nil)
	adapter.SetLogger(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	req := llm.ChatRequest{
		Model:     "test",
		SessionID: "session-1",
		Messages: []llm.LLMMessage{
			{Role: llm.RoleSystem, Segments: llm.TextSegments("system prompt")},
			{Role: llm.RoleUser, Segments: llm.TextSegments("hello")},
		},
	}
	for i := 0; i < 2; i++ {
		ch, err := adapter.ChatStream(context.Background(), req)
		if err != nil {
			t.Fatalf("ChatStream %d: %v", i, err)
		}
		for range ch {
		}
	}

	logText := logs.String()
	if strings.Count(logText, "first_system_message_json=") != 1 {
		t.Fatalf("first system message should be logged once, got logs:\n%s", logText)
	}
	if !strings.Contains(logText, "session_id=session-1") || !strings.Contains(logText, "system_hash=") {
		t.Fatalf("debug log missing session/hash summary: %s", logText)
	}
}

func TestChatStream_SendsMultimodalContentParts(t *testing.T) {
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	adapter := New(srv.URL, "test-key", nil)
	_, err := adapter.ChatStream(context.Background(), llm.ChatRequest{
		Model: "test",
		Messages: []llm.LLMMessage{{Role: llm.RoleUser, Segments: []llm.MessageSegment{
			{Type: llm.SegmentText, Text: "看图"},
			{Type: llm.SegmentImage, URL: "https://example.com/a.png"},
		}}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	var body struct {
		Messages []struct {
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatalf("unmarshal captured body: %v", err)
	}
	if len(body.Messages) != 1 || len(body.Messages[0].Content) != 2 {
		t.Fatalf("content parts = %#v", body.Messages)
	}
	if body.Messages[0].Content[0]["type"] != "text" || body.Messages[0].Content[0]["text"] != "看图" {
		t.Fatalf("text part = %#v", body.Messages[0].Content[0])
	}
	imageURL, ok := body.Messages[0].Content[1]["image_url"].(map[string]any)
	if !ok || imageURL["url"] != "https://example.com/a.png" {
		t.Fatalf("image part = %#v", body.Messages[0].Content[1])
	}
}

func TestChatStream_IncludesEmptyContentField(t *testing.T) {

	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	adapter := New(srv.URL, "test-key", nil)
	_, err := adapter.ChatStream(context.Background(), llm.ChatRequest{
		Model:    "test",
		Messages: []llm.LLMMessage{{Role: llm.RoleAssistant}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	var body struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatalf("unmarshal captured body: %v", err)
	}
	if len(body.Messages) != 1 {
		t.Fatalf("messages len = %d", len(body.Messages))
	}
	content, ok := body.Messages[0]["content"]
	if !ok {
		t.Fatalf("content field missing from %#v", body.Messages[0])
	}
	if content != "" {
		t.Fatalf("content = %#v, want empty string", content)
	}
}

func TestChatStream_ToolMessagesPayload(t *testing.T) {
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	adapter := New(srv.URL, "test-key", nil)
	_, err := adapter.ChatStream(context.Background(), llm.ChatRequest{
		Model: "test",
		Messages: []llm.LLMMessage{
			{Role: llm.RoleAssistant, Segments: llm.TextSegments("I will check."), ToolCalls: []llm.ToolCallRequest{{ID: "call_1", Name: "shell", Arguments: `{"cmd":"ls"}`}}},
			{Role: llm.RoleTool, Name: "shell", ToolCallID: "call_1", Segments: llm.TextSegments("AGENT.md\n")},
		},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	var body struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatalf("unmarshal captured body: %v", err)
	}
	if len(body.Messages) != 2 {
		t.Fatalf("messages len = %d", len(body.Messages))
	}
	calls, ok := body.Messages[0]["tool_calls"].([]any)
	if !ok || len(calls) != 1 {
		t.Fatalf("tool_calls = %#v", body.Messages[0]["tool_calls"])
	}
	call := calls[0].(map[string]any)
	fn := call["function"].(map[string]any)
	if call["id"] != "call_1" || call["type"] != "function" || fn["name"] != "shell" || fn["arguments"] != `{"cmd":"ls"}` {
		t.Fatalf("tool call payload = %#v", call)
	}
	if body.Messages[1]["role"] != "tool" || body.Messages[1]["tool_call_id"] != "call_1" || body.Messages[1]["name"] != "shell" {
		t.Fatalf("tool message payload = %#v", body.Messages[1])
	}
}

func TestChatStream_FirstChunkCanArriveAfterIdleTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		time.Sleep(30 * time.Millisecond)
		io.WriteString(w, `data: {"choices":[{"index":0,"delta":{"content":"late"},"finish_reason":null}]}`+"\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	adapter := NewWithOptions(srv.URL, "test-key", nil, nil, RequestOptions{
		FirstChunkTimeout: 100 * time.Millisecond,
		StreamIdleTimeout: 10 * time.Millisecond,
		MaxRetries:        1,
		RetryInitialDelay: time.Millisecond,
	})
	ch, err := adapter.ChatStream(context.Background(), llm.ChatRequest{
		Model:    "test",
		Messages: []llm.LLMMessage{{Role: llm.RoleUser, Segments: llm.TextSegments("Hi")}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	var got strings.Builder
	for chunk := range ch {
		if chunk.Error != nil {
			t.Fatalf("stream error: %v", chunk.Error)
		}
		got.WriteString(chunk.DeltaContent)
	}
	if got.String() != "late" {
		t.Fatalf("content = %q, want late", got.String())
	}
}

func TestChatStream_StreamIdleTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"choices":[{"index":0,"delta":{"content":"start"},"finish_reason":null}]}`+"\n\n")
		w.(http.Flusher).Flush()
		time.Sleep(50 * time.Millisecond)
	}))
	defer srv.Close()

	adapter := NewWithOptions(srv.URL, "test-key", nil, nil, RequestOptions{
		FirstChunkTimeout: 100 * time.Millisecond,
		StreamIdleTimeout: 10 * time.Millisecond,
		MaxRetries:        1,
		RetryInitialDelay: time.Millisecond,
	})
	ch, err := adapter.ChatStream(context.Background(), llm.ChatRequest{
		Model:    "test",
		Messages: []llm.LLMMessage{{Role: llm.RoleUser, Segments: llm.TextSegments("Hi")}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	var gotText string
	var gotErr error
	for chunk := range ch {
		gotText += chunk.DeltaContent
		if chunk.Error != nil {
			gotErr = chunk.Error
		}
	}
	if gotText != "start" {
		t.Fatalf("text = %q, want start", gotText)
	}
	if gotErr == nil || !strings.Contains(gotErr.Error(), "idle timeout") {
		t.Fatalf("error = %v, want idle timeout", gotErr)
	}
}

func TestChatStream_StreamIdleResetsOnEachChunk(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		chunks := []string{
			`data: {"choices":[{"index":0,"delta":{"content":"a"},"finish_reason":null}]}`,
			`data: {"choices":[{"index":0,"delta":{"content":"b"},"finish_reason":null}]}`,
			`data: {"choices":[{"index":0,"delta":{"content":"c"},"finish_reason":"stop"}]}`,
			`data: [DONE]`,
		}
		for _, c := range chunks {
			io.WriteString(w, c+"\n\n")
			w.(http.Flusher).Flush()
			time.Sleep(10 * time.Millisecond)
		}
	}))
	defer srv.Close()

	adapter := NewWithOptions(srv.URL, "test-key", nil, nil, RequestOptions{
		FirstChunkTimeout: 100 * time.Millisecond,
		StreamIdleTimeout: 30 * time.Millisecond,
		MaxRetries:        1,
		RetryInitialDelay: time.Millisecond,
	})
	ch, err := adapter.ChatStream(context.Background(), llm.ChatRequest{
		Model:    "test",
		Messages: []llm.LLMMessage{{Role: llm.RoleUser, Segments: llm.TextSegments("Hi")}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	var got strings.Builder
	for chunk := range ch {
		if chunk.Error != nil {
			t.Fatalf("stream error: %v", chunk.Error)
		}
		got.WriteString(chunk.DeltaContent)
	}
	if got.String() != "abc" {
		t.Fatalf("content = %q, want abc", got.String())
	}
}

func TestChatStream_ActiveStreamCompletesWithoutResponseTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range []string{
			`data: {"choices":[{"index":0,"delta":{"content":"a"},"finish_reason":null}]}`,
			`data: {"choices":[{"index":0,"delta":{"content":"b"},"finish_reason":null}]}`,
			`data: [DONE]`,
		} {
			io.WriteString(w, c+"\n\n")
			w.(http.Flusher).Flush()
			time.Sleep(20 * time.Millisecond)
		}
	}))
	defer srv.Close()

	adapter := NewWithOptions(srv.URL, "test-key", nil, nil, RequestOptions{
		FirstChunkTimeout: 100 * time.Millisecond,
		StreamIdleTimeout: 50 * time.Millisecond,
		MaxRetries:        1,
		RetryInitialDelay: time.Millisecond,
	})
	ch, err := adapter.ChatStream(context.Background(), llm.ChatRequest{
		Model:    "test",
		Messages: []llm.LLMMessage{{Role: llm.RoleUser, Segments: llm.TextSegments("Hi")}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	var got strings.Builder
	for chunk := range ch {
		if chunk.Error != nil {
			t.Fatalf("stream error: %v", chunk.Error)
		}
		got.WriteString(chunk.DeltaContent)
	}
	if got.String() != "ab" {
		t.Fatalf("content = %q, want ab", got.String())
	}
}

func TestChatStream_UsageOnlyChunk(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		chunks := []string{
			`data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}`,
			`data: {"id":"usage","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7,"prompt_tokens_details":{"cached_tokens":2}}}`,
			`data: [DONE]`,
		}
		for _, c := range chunks {
			io.WriteString(w, c+"\n\n")
			w.(http.Flusher).Flush()
		}
	}))
	defer srv.Close()

	adapter := New(srv.URL, "test-key", nil)
	ch, err := adapter.ChatStream(context.Background(), llm.ChatRequest{
		Model:    "test",
		Messages: []llm.LLMMessage{{Role: llm.RoleUser, Segments: llm.TextSegments("Hi")}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	var content strings.Builder
	var usage *llm.Usage
	for chunk := range ch {
		if chunk.Error != nil {
			t.Fatalf("stream error: %v", chunk.Error)
		}
		content.WriteString(chunk.DeltaContent)
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
	}

	if content.String() != "Hello" {
		t.Errorf("expected content Hello, got %q", content.String())
	}
	if usage == nil {
		t.Fatal("expected usage-only chunk")
	}
	if usage.TotalTokens != 7 {
		t.Errorf("expected total_tokens=7, got %d", usage.TotalTokens)
	}
	if usage.CacheHitTokens != 2 {
		t.Errorf("expected cache hit tokens=2, got %d", usage.CacheHitTokens)
	}
}

func TestChatStream_UsageCacheHitAliases(t *testing.T) {
	for name, body := range map[string]string{
		"top level cache hit": `{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7,"prompt_cache_hit_tokens":5}`,
		"input details hit":   `{"input_tokens":3,"output_tokens":4,"total_tokens":7,"input_tokens_details":{"cache_hit_tokens":6}}`,
	} {
		t.Run(name, func(t *testing.T) {
			var usage openAIUsage
			if err := json.Unmarshal([]byte(body), &usage); err != nil {
				t.Fatalf("unmarshal usage: %v", err)
			}
			got := toUsage(&usage)
			if got.TotalTokens != 7 || got.CacheHitTokens == 0 {
				t.Fatalf("usage = %#v", got)
			}
		})
	}
}

func TestListModelMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"id":"alpha","context_window":32000},{"id":"beta","max_input_tokens":16000}]}`)
	}))
	defer srv.Close()

	adapter := New(srv.URL, "test-key", nil)
	models, err := adapter.ListModelMetadata(context.Background())
	if err != nil {
		t.Fatalf("ListModelMetadata: %v", err)
	}
	if len(models) != 2 || models[0].ContextWindow != 32000 || models[1].ContextWindow != 16000 {
		t.Fatalf("metadata = %#v", models)
	}
}

func TestChatStream_MalformedChunkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {not-json}\n\n")
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	adapter := New(srv.URL, "test-key", nil)
	ch, err := adapter.ChatStream(context.Background(), llm.ChatRequest{
		Model:    "test",
		Messages: []llm.LLMMessage{{Role: llm.RoleUser, Segments: llm.TextSegments("Hi")}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	chunk, ok := <-ch
	if !ok {
		t.Fatal("expected error chunk")
	}
	if chunk.Error == nil {
		t.Fatal("expected stream parse error")
	}
	if !strings.Contains(chunk.Error.Error(), "parse stream chunk") {
		t.Fatalf("expected parse stream chunk error, got %v", chunk.Error)
	}
}

func TestChatStream_ToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		chunks := []string{
			`data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"get","arguments":"wea"}}]},"finish_reason":null}]}`,
			`data: {"id":"2","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"","function":{"name":"","arguments":"ther"}}]},"finish_reason":"tool_calls"}]}`,
			`data: [DONE]`,
		}
		for _, c := range chunks {
			io.WriteString(w, c+"\n\n")
			w.(http.Flusher).Flush()
		}
	}))
	defer srv.Close()

	adapter := New(srv.URL, "test-key", nil)
	ch, err := adapter.ChatStream(context.Background(), llm.ChatRequest{
		Model:    "test",
		Messages: []llm.LLMMessage{{Role: llm.RoleUser, Segments: llm.TextSegments("Hi")}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	var toolCalls []llm.ToolCallDelta
	for chunk := range ch {
		if len(chunk.ToolCallDeltas) > 0 {
			toolCalls = append(toolCalls, chunk.ToolCallDeltas...)
		}
	}

	if len(toolCalls) != 1 {
		t.Fatalf("expected 1 accumulated tool call, got %d", len(toolCalls))
	}
	tc := toolCalls[0]
	if tc.Name != "get" {
		t.Errorf("expected tool name 'get', got %q", tc.Name)
	}
	if tc.Args != "weather" {
		t.Errorf("expected args 'weather', got %q", tc.Args)
	}
}

func TestChatStream_ToolsPayload(t *testing.T) {
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	adapter := New(srv.URL, "test-key", nil)
	_, err := adapter.ChatStream(context.Background(), llm.ChatRequest{
		Model:    "test",
		Messages: []llm.LLMMessage{{Role: llm.RoleUser, Segments: llm.TextSegments("Hi")}},
		Tools: []llm.ToolSchema{{Function: llm.ToolFunctionSchema{
			Name:        "discover_tool",
			Description: "Discover tools.",
			Parameters:  map[string]any{"type": "object"},
		}}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatalf("unmarshal captured body: %v", err)
	}
	tools, ok := body["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %#v", body["tools"])
	}
	tool := tools[0].(map[string]any)
	fn := tool["function"].(map[string]any)
	if tool["type"] != "function" || fn["name"] != "discover_tool" {
		t.Fatalf("tool payload = %#v", tool)
	}
}

func TestChatStream_OmitsEmptyToolsPayload(t *testing.T) {
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	adapter := New(srv.URL, "test-key", nil)
	_, err := adapter.ChatStream(context.Background(), llm.ChatRequest{
		Model:    "test",
		Messages: []llm.LLMMessage{{Role: llm.RoleUser, Segments: llm.TextSegments("Hi")}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatalf("unmarshal captured body: %v", err)
	}
	if _, ok := body["tools"]; ok {
		t.Fatalf("unexpected tools payload: %#v", body["tools"])
	}
}

func TestRetryDelay(t *testing.T) {
	initial := 2 * time.Second
	want := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}
	for attempt, expected := range want {
		if got := retryDelay(initial, attempt); got != expected {
			t.Fatalf("attempt %d delay = %s, want %s", attempt, got, expected)
		}
	}
}

func TestChatStream_RetriesRetryableHTTPStatus(t *testing.T) {
	attempts := 0
	retryEvents := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusBadGateway)
			io.WriteString(w, `{"error":{"message":"temporary upstream error"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	adapter := NewWithOptions(srv.URL, "test-key", nil, nil, RequestOptions{
		MaxRetries:        3,
		RetryInitialDelay: time.Millisecond,
		OnRetry: func(ctx context.Context, event RetryEvent) {
			retryEvents++
			if event.Attempt != retryEvents {
				t.Fatalf("retry attempt = %d, want %d", event.Attempt, retryEvents)
			}
			if event.MaxRetries != 3 {
				t.Fatalf("max retries = %d, want 3", event.MaxRetries)
			}
			if event.Err == nil {
				t.Fatal("expected retry error")
			}
		},
	})
	ch, err := adapter.ChatStream(context.Background(), llm.ChatRequest{
		Model:    "test",
		Messages: []llm.LLMMessage{{Role: llm.RoleUser, Segments: llm.TextSegments("Hi")}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	for range ch {
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
	if retryEvents != 2 {
		t.Fatalf("retry events = %d, want 2", retryEvents)
	}
}

func TestChatStream_ReportsMissingDoneAsInterrupted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"choices":[{"index":0,"delta":{"content":"partial"},"finish_reason":null}]}`+"\n\n")
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	adapter := NewWithOptions(srv.URL, "test-key", nil, nil, RequestOptions{MaxRetries: 1, RetryInitialDelay: time.Millisecond})
	ch, err := adapter.ChatStream(context.Background(), llm.ChatRequest{
		Model:    "test",
		Messages: []llm.LLMMessage{{Role: llm.RoleUser, Segments: llm.TextSegments("Hi")}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	var gotText string
	var gotErr error
	for chunk := range ch {
		gotText += chunk.DeltaContent
		if chunk.Error != nil {
			gotErr = chunk.Error
		}
	}
	if gotText != "partial" {
		t.Fatalf("text = %q, want partial", gotText)
	}
	if gotErr == nil {
		t.Fatal("expected missing [DONE] error")
	}
	if !strings.Contains(gotErr.Error(), "unexpected EOF") {
		t.Fatalf("error = %v, want unexpected EOF", gotErr)
	}
}
func TestChatStream_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"invalid api key","type":"auth_error"}}`)
	}))
	defer srv.Close()

	adapter := New(srv.URL, "bad-key", nil)
	_, err := adapter.ChatStream(context.Background(), llm.ChatRequest{
		Model:    "test",
		Messages: []llm.LLMMessage{{Role: llm.RoleUser, Segments: llm.TextSegments("Hi")}},
	})
	if err == nil {
		t.Fatal("expected error for 401")
	}
	if !strings.Contains(err.Error(), "invalid api key") {
		t.Errorf("expected 'invalid api key' in error, got: %v", err)
	}
}

func TestChatStream_ExtraBody(t *testing.T) {
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	adapter := New(srv.URL, "test-key", map[string]any{
		"custom_provider_field": "from_provider",
		"overridden":            "provider_value",
	})

	_, err := adapter.ChatStream(context.Background(), llm.ChatRequest{
		Model: "test-model",
		Messages: []llm.LLMMessage{
			{Role: llm.RoleUser, Segments: llm.TextSegments("Hi")},
		},
		Temperature: 0.7,
		MaxTokens:   100,
		ExtraBody: map[string]any{
			"custom_request_field": "from_request",
			"overridden":           "request_value",
		},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatalf("unmarshal captured body: %v", err)
	}

	// Standard fields
	if body["model"] != "test-model" {
		t.Errorf("model: got %v", body["model"])
	}
	if body["temperature"] != 0.7 {
		t.Errorf("temperature: got %v", body["temperature"])
	}

	// ExtraPayload present
	if body["custom_provider_field"] != "from_provider" {
		t.Errorf("custom_provider_field: got %v", body["custom_provider_field"])
	}

	// ExtraBody present
	if body["custom_request_field"] != "from_request" {
		t.Errorf("custom_request_field: got %v", body["custom_request_field"])
	}

	// ExtraBody overrides ExtraPayload
	if body["overridden"] != "request_value" {
		t.Errorf("overridden: expected 'request_value', got %v", body["overridden"])
	}
}

func TestChatStream_ModelExtraPayload(t *testing.T) {
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	adapter := NewWithModelExtraPayloads(srv.URL, "test-key", map[string]any{
		"provider_field": "provider",
		"overridden":     "provider_value",
	}, map[string]map[string]any{
		"test-model": {
			"thinking":       map[string]any{"type": "disabled"},
			"overridden":     "model_value",
			"model_only_key": "model_only_value",
		},
	})

	_, err := adapter.ChatStream(context.Background(), llm.ChatRequest{
		Model: "test-model",
		Messages: []llm.LLMMessage{
			{Role: llm.RoleUser, Segments: llm.TextSegments("Hi")},
		},
		ExtraBody: map[string]any{
			"overridden": "request_value",
		},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatalf("unmarshal captured body: %v", err)
	}

	if body["provider_field"] != "provider" {
		t.Errorf("provider_field: got %v", body["provider_field"])
	}
	if body["model_only_key"] != "model_only_value" {
		t.Errorf("model_only_key: got %v", body["model_only_key"])
	}
	thinking, ok := body["thinking"].(map[string]any)
	if !ok || thinking["type"] != "disabled" {
		t.Errorf("thinking: got %#v", body["thinking"])
	}
	if body["overridden"] != "request_value" {
		t.Errorf("overridden: expected request_value, got %v", body["overridden"])
	}
}
