package responses

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"elbot/internal/llm"
)

func mustClient(t *testing.T, url string, extras map[string]any, modelExtras map[string]map[string]any, opts RequestOptions) *Client {
	t.Helper()
	client, err := New(url, "secret-key", extras, modelExtras, opts)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func emit(w http.ResponseWriter, event string, raw string) {
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, raw)
	w.(http.Flusher).Flush()
}

const completedText = `{"type":"response.completed","response":{"id":"resp-1","status":"completed","model":"m","output":[{"type":"message","id":"msg-1","role":"assistant","content":[{"type":"output_text","text":"title","annotations":[{"future":true}]}]}],"usage":{"input_tokens":9,"output_tokens":2,"total_tokens":11,"input_tokens_details":{"cached_tokens":4}}}}`

func TestGenerateTextUsesNativeRequestAndCompleteResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if r.URL.Path != "/responses" || r.Header.Get("Authorization") != "Bearer secret-key" || body["store"] != false || body["max_output_tokens"] != float64(32) || body["instructions"] != "instructions" {
			t.Errorf("path=%s body=%v", r.URL.Path, body)
		}
		if body["reasoning"].(map[string]any)["effort"] != "medium" || body["text"].(map[string]any)["format"].(map[string]any)["type"] != "json_object" || body["seed"] != float64(7) {
			t.Errorf("extras=%v", body)
		}
		for _, key := range []string{"messages", "tools", "previous_response_id"} {
			if _, exists := body[key]; exists {
				t.Errorf("unexpected field %s", key)
			}
		}
		input := body["input"].([]any)[0].(map[string]any)
		if input["role"] != "user" || input["content"].([]any)[0].(map[string]any)["text"] != "input" {
			t.Errorf("input=%v", input)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		emit(w, "response.output_text.delta", `{"type":"response.output_text.delta","delta":"partial text should not replace final"}`)
		emit(w, "response.completed", completedText)
	}))
	defer srv.Close()
	client := mustClient(t, srv.URL, map[string]any{"reasoning": map[string]any{"effort": "medium"}}, map[string]map[string]any{"m": {"text": map[string]any{"format": map[string]any{"type": "json_object"}}}}, RequestOptions{})
	result, err := client.GenerateText(context.Background(), llm.TextRequest{Model: "m", Instructions: "instructions", Input: "input", MaxOutputTokens: 32, ExtraBody: map[string]any{"seed": 7}})
	if err != nil || result.Text != "title" || result.Usage == nil || result.Usage.CacheHitTokens != 4 || result.Usage.TotalTokens != 11 {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}

func TestStreamRetainsNativeItemsAndFunctionEvents(t *testing.T) {
	reasoning := `{"type":"reasoning","id":"rs-1","encrypted_content":"opaque-state","summary":[],"future":{"keep":true}}`
	function := `{"type":"function_call","id":"fc-1","call_id":"call-1","name":"read_file","arguments":"{\"path\":\"x\"}","status":"completed"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if _, exists := body["tools"]; exists {
			t.Error("top-level tool definitions sent")
		}
		input := body["input"].([]any)[0].(map[string]any)
		if input["type"] != "additional_tools" || input["role"] != "developer" {
			t.Errorf("tool input=%v", input)
		}
		tools := input["tools"].([]any)
		tool := tools[0].(map[string]any)
		if tool["type"] != "function" || tool["name"] != "read_file" || tool["strict"] != false || body["previous_response_id"] != "previous" {
			t.Errorf("body=%v", body)
		}
		if _, nested := tool["function"]; nested {
			t.Error("Chat tool wrapper used")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		emit(w, "response.created", `{"type":"response.created","response":{"id":"resp-1","status":"in_progress"}}`)
		emit(w, "response.output_item.done", `{"type":"response.output_item.done","output_index":0,"item":`+reasoning+`}`)
		emit(w, "response.reasoning_summary_text.delta", `{"type":"response.reasoning_summary_text.delta","delta":"summary"}`)
		emit(w, "response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","item_id":"fc-1","output_index":1,"delta":"{\"path\":"}`)
		emit(w, "response.function_call_arguments.done", `{"type":"response.function_call_arguments.done","item_id":"fc-1","output_index":1,"arguments":"{\"path\":\"x\"}"}`)
		emit(w, "response.output_item.done", `{"type":"response.output_item.done","output_index":1,"item":`+function+`}`)
		emit(w, "response.future", `{"type":"response.future","delta":{"arbitrary":true}}`)
		emit(w, "response.completed", `{"type":"response.completed","response":{"id":"resp-1","status":"completed","output":[`+reasoning+`,`+function+`],"future_response":42}}`)
	}))
	defer srv.Close()
	client := mustClient(t, srv.URL, nil, nil, RequestOptions{})
	definitions, err := AdditionalTools(FunctionTools([]llm.ToolSchema{{Name: "read_file", Parameters: map[string]any{"type": "object"}}}))
	if err != nil {
		t.Fatal(err)
	}
	events, err := client.Stream(context.Background(), Request{Model: "m", PreviousResponseID: "previous", Input: []Item{definitions}, AllowedTools: []string{"read_file"}})
	if err != nil {
		t.Fatal(err)
	}
	var all []Event
	for event := range events {
		if event.Error != nil {
			t.Fatal(event.Error)
		}
		all = append(all, event)
	}
	if len(all) != 8 || all[3].Delta != "{\"path\":" || all[4].Arguments != "{\"path\":\"x\"}" || all[5].Item.CallID != "call-1" {
		t.Fatalf("events=%+v", all)
	}
	last := all[len(all)-1]
	if last.Response == nil || len(last.Response.Output) != 2 || string(last.Response.Output[0].Raw) != reasoning || !bytes.Contains(last.Response.Raw, []byte("future_response")) {
		t.Fatalf("response=%+v", last.Response)
	}
	encoded, err := json.Marshal(last.Response.Output[0])
	if err != nil || !bytes.Contains(encoded, []byte("opaque-state")) || !bytes.Contains(encoded, []byte("future")) {
		t.Fatalf("native item lost: %s error=%v", encoded, err)
	}
}

func TestStreamRejectsFailedIncompleteAndPrematureEOF(t *testing.T) {
	for _, tc := range []struct{ name, event, raw, want string }{
		{"failed", "response.failed", `{"type":"response.failed","response":{"id":"r","status":"failed","error":{"code":"server_error","message":"failed generation"}}}`, "failed generation"},
		{"incomplete", "response.incomplete", `{"type":"response.incomplete","response":{"id":"r","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`, "max_output_tokens"},
		{"error", "error", `{"type":"error","code":"bad","message":"bad event"}`, "bad event"},
		{"invalid completed", "response.completed", `{"type":"response.completed","response":{"id":"r","status":"in_progress"}}`, "invalid completed"},
		{"EOF", "response.output_text.delta", `{"type":"response.output_text.delta","delta":"partial"}`, "unexpected EOF"},
		{"bad JSON", "response.created", `{broken`, "parse response event"},
		{"mismatched event", "response.completed", `{"type":"response.in_progress"}`, "invalid response event type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				emit(w, tc.event, tc.raw)
			}))
			defer srv.Close()
			client := mustClient(t, srv.URL, nil, nil, RequestOptions{RetryInitialDelay: time.Millisecond})
			_, err := client.GenerateText(context.Background(), llm.TextRequest{Model: "m", Input: "input"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v want=%s", err, tc.want)
			}
			if calls.Load() != 1 {
				t.Fatal("failed stream was replayed")
			}
		})
	}
}

func TestGenerateTextRejectsRefusalAndToolCalls(t *testing.T) {
	for _, output := range []string{
		`[{"type":"message","content":[{"type":"refusal","refusal":"cannot answer"}]}]`,
		`[{"type":"function_call","call_id":"c","name":"shell","arguments":"{}"}]`,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			emit(w, "response.completed", `{"type":"response.completed","response":{"id":"r","status":"completed","output":`+output+`}}`)
		}))
		client := mustClient(t, srv.URL, nil, nil, RequestOptions{})
		if _, err := client.GenerateText(context.Background(), llm.TextRequest{Model: "m", Input: "input"}); err == nil {
			t.Fatal("unexpected independent text result accepted")
		}
		srv.Close()
	}
}

func TestResponseExtrasCannotReplaceOwnedFieldsOrEachOther(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer srv.Close()
	for _, field := range []string{"model", "input", "instructions", "tools", "stream", "previous_response_id", "conversation", "store"} {
		t.Run(field, func(t *testing.T) {
			client := mustClient(t, srv.URL, map[string]any{field: "override"}, nil, RequestOptions{})
			if _, err := client.Stream(context.Background(), Request{Model: "m"}); err == nil || !strings.Contains(err.Error(), field) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	client := mustClient(t, srv.URL, map[string]any{"seed": 1}, map[string]map[string]any{"m": {"seed": 1}}, RequestOptions{})
	if _, err := client.Stream(context.Background(), Request{Model: "m"}); err == nil {
		t.Fatal("duplicate model field accepted")
	}
	client = mustClient(t, srv.URL, nil, map[string]map[string]any{"m": {"seed": 1}}, RequestOptions{})
	if _, err := client.Stream(context.Background(), Request{Model: "m", ExtraBody: map[string]any{"seed": 2}}); err == nil {
		t.Fatal("duplicate request field accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("conflicting payload reached upstream")
	}
}

func TestResponseTimeoutsAndCancellation(t *testing.T) {
	for _, first := range []bool{true, false} {
		t.Run(fmt.Sprint("first=", first), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.(http.Flusher).Flush()
				if !first {
					emit(w, "response.created", `{"type":"response.created","response":{"id":"r","status":"in_progress"}}`)
				}
				<-r.Context().Done()
			}))
			defer srv.Close()
			client := mustClient(t, srv.URL, nil, nil, RequestOptions{FirstChunkTimeout: 40 * time.Millisecond, StreamIdleTimeout: 40 * time.Millisecond})
			_, err := client.GenerateText(context.Background(), llm.TextRequest{Model: "m", Input: "input"})
			want := "idle timeout"
			if first {
				want = "first stream chunk timeout"
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		emit(w, "response.created", `{"type":"response.created"}`)
		close(started)
		<-r.Context().Done()
	}))
	defer srv.Close()
	client := mustClient(t, srv.URL, nil, nil, RequestOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { _, err := client.GenerateText(ctx, llm.TextRequest{Model: "m", Input: "input"}); errCh <- err }()
	<-started
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("text generation did not stop")
	}
}

func TestResponseMetadataAndRetryKeepSharedNotifier(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			io.WriteString(w, `{"data":[{"id":"m","context_window":1000}]}`)
			return
		}
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			io.WriteString(w, `{"error":{"message":"try again"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		emit(w, "response.completed", completedText)
	}))
	defer srv.Close()
	client := mustClient(t, srv.URL, nil, nil, RequestOptions{RetryInitialDelay: time.Millisecond})
	var retries []llm.RetryEvent
	client.SetRetryNotifier(func(ctx context.Context, event llm.RetryEvent) {
		if ctx.Err() != nil {
			t.Error("retry context canceled")
		}
		retries = append(retries, event)
	})
	models, err := client.ListModels(context.Background())
	if err != nil || len(models) != 1 || models[0] != "m" {
		t.Fatalf("models=%v error=%v", models, err)
	}
	metadata, err := client.ListModelMetadata(context.Background())
	if err != nil || len(metadata) != 1 || metadata[0].ContextWindow != 1000 {
		t.Fatalf("metadata=%v error=%v", metadata, err)
	}
	if _, err := client.GenerateText(context.Background(), llm.TextRequest{Model: "m"}); err != nil {
		t.Fatal(err)
	}
	if len(retries) != 1 || retries[0].Attempt != 1 || requests.Load() != 2 {
		t.Fatalf("retries=%+v calls=%d", retries, requests.Load())
	}
}

func TestResponseLogsExcludeInputAndOpaqueReasoning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		emit(w, "response.completed", completedText)
	}))
	defer srv.Close()
	client := mustClient(t, srv.URL, nil, nil, RequestOptions{})
	var logs bytes.Buffer
	client.SetLogger(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	if _, err := client.GenerateText(context.Background(), llm.TextRequest{Model: "m", Instructions: "private instructions", Input: "private input"}); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"secret-key", "private input", "private instructions", "encrypted_content"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("sensitive log=%s", logs.String())
		}
	}
}

func TestNativeInputAndUnknownItemsPreserveTheirJSON(t *testing.T) {
	item, err := InputMessage("user", []InputContent{{Type: "input_image", ImageURL: "data:image/png;base64,eA=="}, {Type: "input_file", FileData: "data:text/plain;base64,eA==", Filename: "x.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(item.Raw, []byte("input_image")) || !bytes.Contains(item.Raw, []byte("filename")) {
		t.Fatalf("input=%s", item.Raw)
	}
	output, err := FunctionCallOutput("call", json.RawMessage(`[{"type":"input_text","text":"done"},{"type":"input_image","image_url":"https://example.invalid/image"}]`))
	if err != nil || !bytes.Contains(output.Raw, []byte("call_id")) || !bytes.Contains(output.Raw, []byte("input_image")) {
		t.Fatalf("output=%s error=%v", output.Raw, err)
	}
	unknown := `{"type":"future_native_item","id":"future","content":{"opaque":true}}`
	parsed, err := ParseItem([]byte(unknown))
	if err != nil || string(parsed.Raw) != unknown {
		t.Fatalf("item=%+v error=%v", parsed, err)
	}
	if _, err := ParseItem([]byte(`[]`)); err == nil {
		t.Fatal("non-object item accepted")
	}
	unknownPart := `{"type":"message","content":[{"type":"future_part","text":{"opaque":true}}]}`
	parsed, err = ParseItem([]byte(unknownPart))
	if err != nil || string(parsed.Raw) != unknownPart || len(parsed.Content) != 1 || parsed.Content[0].Type != "future_part" {
		t.Fatalf("native content part lost: %+v error=%v", parsed, err)
	}
}
