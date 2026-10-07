package responses

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const compactItemJSON = `{"type":"compaction","encrypted_content":"opaque","future":9007199254740993}`

func emitCompactResponse(w http.ResponseWriter, items []string, done, terminalOutput bool) {
	w.Header().Set("Content-Type", "text/event-stream")
	if done {
		for i, item := range items {
			emit(w, "response.output_item.done", fmt.Sprintf(`{"type":"response.output_item.done","output_index":%d,"item":%s}`, i, item))
		}
	}
	output := "[]"
	if terminalOutput {
		output = "[" + strings.Join(items, ",") + "]"
	}
	emit(w, "response.completed", `{"type":"response.completed","response":{"id":"compact","status":"completed","output":`+output+`,"usage":{"input_tokens":10,"output_tokens":3,"total_tokens":13}}}`)
}

func TestCompactUsesFrozenResponsesRequest(t *testing.T) {
	var frozen []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || r.URL.Path != "/responses" || r.Header.Get("Authorization") != "Bearer secret-key" || !bytes.Equal(raw, frozen) {
			t.Errorf("request=%s %s %s", r.Method, r.URL.Path, raw)
		}
		emitCompactResponse(w, []string{`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"audit only"}]}`, compactItemJSON}, true, true)
	}))
	defer server.Close()
	provider := map[string]any{"reasoning": map[string]any{"effort": "high"}, "service_tier": "default", "store": true, "tool_choice": "required"}
	model := map[string]any{"text": map[string]any{"format": map[string]any{"type": "json_object"}}, "prompt_cache_key": "cache", "include": []string{"reasoning.encrypted_content", "message.output_text.logprobs"}}
	client := mustClient(t, server.URL, provider, map[string]map[string]any{"m": model}, RequestOptions{})
	item, err := ParseItem([]byte(`{"type":"reasoning","encrypted_content":"input-opaque","unknown":9007199254740993}`))
	if err != nil {
		t.Fatal(err)
	}
	input := make([]Item, 1, 2)
	input[0] = item
	extras := map[string]any{"vendor_option": "original"}
	prepared, err := client.PrepareCompact(CompactRequest{Model: "m", Instructions: "fresh instructions", Input: input, ExtraBody: extras})
	if err != nil {
		t.Fatal(err)
	}
	frozen = prepared.JSON()
	extras["vendor_option"] = "mutated"
	input[0].Raw[0] = '!'
	copyBytes := prepared.JSON()
	copyBytes[0] = '!'
	var body map[string]json.RawMessage
	if err := json.Unmarshal(frozen, &body); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"store": "false", "stream": "true", "tool_choice": `"none"`, "model": `"m"`, "instructions": `"fresh instructions"`} {
		if string(body[key]) != want {
			t.Errorf("%s=%s want=%s", key, body[key], want)
		}
	}
	for _, unwanted := range []string{"text", "tools", "previous_response_id", "conversation"} {
		if _, exists := body[unwanted]; exists {
			t.Errorf("unwanted field %s", unwanted)
		}
	}
	var inputs []Item
	if err := json.Unmarshal(body["input"], &inputs); err != nil || len(inputs) != 2 || string(inputs[1].Raw) != `{"type":"compaction_trigger"}` {
		t.Fatalf("input=%s err=%v", body["input"], err)
	}
	for _, wanted := range []string{"9007199254740993", `"effort":"high"`, `"prompt_cache_key":"cache"`, `"service_tier":"default"`, `"vendor_option":"original"`} {
		if !bytes.Contains(frozen, []byte(wanted)) {
			t.Errorf("request missing %s: %s", wanted, frozen)
		}
	}
	if strings.Count(string(body["include"]), "reasoning.encrypted_content") != 1 || !bytes.Contains(body["include"], []byte("message.output_text.logprobs")) {
		t.Errorf("include=%s", body["include"])
	}
	if provider["tool_choice"] != "required" || model["text"] == nil {
		t.Fatal("compaction mutated normal inference configuration")
	}
	result, err := client.CompactPrepared(t.Context(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Compaction.Raw) != compactItemJSON || len(result.Output) != 2 || result.TokenUsage().TotalTokens != 13 || !bytes.Contains(result.Raw, []byte("audit only")) {
		t.Fatalf("result=%+v raw=%s", result, result.Raw)
	}
}

func TestCompactRejectsControlledOverridesAndRetainedTriggers(t *testing.T) {
	client := mustClient(t, "http://unused.invalid", nil, nil, RequestOptions{})
	for _, key := range []string{"model", "input", "instructions", "store", "stream", "previous_response_id", "conversation", "tools", "tool_choice", "text"} {
		t.Run(key, func(t *testing.T) {
			if _, err := client.PrepareCompact(CompactRequest{Model: "m", ExtraBody: map[string]any{key: nil}}); err == nil {
				t.Fatalf("accepted override %s", key)
			}
		})
	}
	trigger, _ := ParseItem([]byte(`{"type":"compaction_trigger"}`))
	if _, err := client.PrepareCompact(CompactRequest{Model: "m", Input: []Item{trigger}}); err == nil {
		t.Fatal("accepted a trigger in retained input")
	}
}

func TestCompactAcceptsCompletedOutputWithoutCountingItTwice(t *testing.T) {
	for _, mode := range []string{"items and terminal", "terminal only", "items and sparse terminal"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				emitCompactResponse(w, []string{compactItemJSON}, mode != "terminal only", mode != "items and sparse terminal")
			}))
			defer server.Close()
			result, err := mustClient(t, server.URL, nil, nil, RequestOptions{}).Compact(t.Context(), CompactRequest{Model: "m"})
			if err != nil || string(result.Compaction.Raw) != compactItemJSON || len(result.Output) != 1 {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestCompactRejectsInvalidOutputAndUnsuccessfulStreams(t *testing.T) {
	for _, tc := range []struct {
		name, event, raw, want string
		items                  []string
	}{
		{name: "missing", items: []string{`{"type":"reasoning","encrypted_content":"x"}`}, want: "exactly one"},
		{name: "duplicate", items: []string{compactItemJSON, compactItemJSON}, want: "exactly one"},
		{name: "missing encrypted", items: []string{`{"type":"compaction"}`}, want: "encrypted_content"},
		{name: "empty encrypted", items: []string{`{"type":"compaction","encrypted_content":"  "}`}, want: "encrypted_content"},
		{name: "invalid encrypted", items: []string{`{"type":"compaction","encrypted_content":3}`}, want: "encrypted_content"},
		{name: "failed", event: "response.failed", raw: `{"type":"response.failed","response":{"id":"r","status":"failed","error":{"message":"compact failed"}}}`, want: "compact failed"},
		{name: "incomplete", event: "response.incomplete", raw: `{"type":"response.incomplete","response":{"id":"r","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`, want: "max_output_tokens"},
		{name: "API error", event: "error", raw: `{"type":"error","code":"bad","message":"bad compact"}`, want: "bad compact"},
		{name: "invalid terminal", event: "response.completed", raw: `{"type":"response.completed","response":{"id":"r","status":"in_progress"}}`, want: "invalid completed"},
		{name: "EOF", want: "unexpected EOF"},
		{name: "404", want: "404"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.URL.Path != "/responses" {
					t.Errorf("unexpected endpoint %s", r.URL.Path)
				}
				if tc.name == "404" {
					w.WriteHeader(http.StatusNotFound)
					fmt.Fprint(w, `{"detail":"Not Found"}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if tc.items != nil {
					emitCompactResponse(w, tc.items, true, true)
					return
				}
				emit(w, "response.output_item.done", `{"type":"response.output_item.done","item":`+compactItemJSON+`}`)
				if tc.event != "" {
					emit(w, tc.event, tc.raw)
				}
			}))
			defer server.Close()
			result, err := mustClient(t, server.URL, nil, nil, RequestOptions{}).Compact(t.Context(), CompactRequest{Model: "m"})
			if err == nil || !strings.Contains(err.Error(), tc.want) || requests.Load() != 1 {
				t.Fatalf("err=%v want=%s requests=%d", err, tc.want, requests.Load())
			}
			if tc.name != "404" && (result == nil || len(result.Output) == 0 || result.Compaction.Type != "") {
				t.Fatalf("partial output not retained for audit: %+v", result)
			}
		})
	}
}

func TestCompactCancellationAfterOutputDoesNotSucceed(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		emit(w, "response.output_item.done", `{"type":"response.output_item.done","item":`+compactItemJSON+`}`)
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	client := mustClient(t, server.URL, nil, nil, RequestOptions{})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := client.Compact(ctx, CompactRequest{Model: "m"})
		done <- err
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled compact did not finish")
	}
}
