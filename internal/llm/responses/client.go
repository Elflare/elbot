package responses

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"strings"

	"elbot/internal/llm"
	"elbot/internal/llm/httpclient"
)

var _ Streamer = (*Client)(nil)

type RequestOptions = httpclient.Options

type Streamer interface {
	llm.Client
	StoreForModel(string) (bool, error)
	Stream(context.Context, Request) (<-chan Event, error)
	PrepareRequest(Request) (PreparedRequest, error)
	StreamPrepared(context.Context, PreparedRequest) (<-chan Event, error)
}

// PreparedRequest freezes the fully merged native body without credentials.
type PreparedRequest struct {
	body         []byte
	model        string
	inputs       int
	allowedTools []string
}

func (p PreparedRequest) JSON() json.RawMessage  { return append(json.RawMessage(nil), p.body...) }
func (p PreparedRequest) AllowedTools() []string { return append([]string(nil), p.allowedTools...) }

type Client struct {
	baseURL            string
	apiKey             string
	extraPayload       map[string]any
	modelExtraPayloads map[string]map[string]any
	transport          *httpclient.Client
	logger             *slog.Logger
}

func New(baseURL, apiKey string, extras map[string]any, modelExtras map[string]map[string]any, opts RequestOptions) (*Client, error) {
	transport, err := httpclient.New(opts)
	if err != nil {
		return nil, err
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, extraPayload: extras, modelExtraPayloads: modelExtras, transport: transport}, nil
}

func (c *Client) SetLogger(logger *slog.Logger) { c.logger = logger }
func (c *Client) SetRetryNotifier(f func(context.Context, llm.RetryEvent)) {
	c.transport.SetRetryNotifier(f)
}

func (c *Client) validateBaseURL() error {
	if strings.TrimSpace(c.baseURL) == "" {
		return errors.New("response base_url is required")
	}
	return nil
}

func (c *Client) Stream(ctx context.Context, req Request) (<-chan Event, error) {
	prepared, err := c.PrepareRequest(req)
	if err != nil {
		return nil, err
	}
	return c.StreamPrepared(ctx, prepared)
}

func (c *Client) PrepareRequest(req Request) (PreparedRequest, error) {
	return c.prepareRequest(req, false)
}

func (c *Client) prepareRequest(req Request, compact bool) (PreparedRequest, error) {
	if strings.TrimSpace(c.baseURL) == "" {
		return PreparedRequest{}, errors.New("response base_url is required")
	}
	store, err := c.StoreForModel(req.Model)
	if err != nil {
		return PreparedRequest{}, err
	}
	input := req.Input
	if input == nil {
		input = []Item{}
	}
	body := map[string]any{"model": req.Model, "input": input, "stream": true}
	if req.Instructions != "" {
		body["instructions"] = req.Instructions
	}
	if req.PreviousResponseID != "" {
		body["previous_response_id"] = req.PreviousResponseID
	}
	if req.Store != nil {
		store = *req.Store
	}
	body["store"] = store
	if req.MaxOutputTokens > 0 {
		body["max_output_tokens"] = req.MaxOutputTokens
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	providerExtra, modelExtra := withoutStore(c.extraPayload), withoutStore(c.modelExtraPayloads[req.Model])
	reserved := []string{"model", "input", "instructions", "tools", "stream", "previous_response_id", "conversation", "store"}
	if compact {
		// Compaction inherits inference/cache options, but neither an answer format
		// nor a configured tool choice may turn it into a normal generation.
		for _, extra := range []map[string]any{providerExtra, modelExtra} {
			delete(extra, "text")
			delete(extra, "tool_choice")
		}
		body["tool_choice"] = "none"
		reserved = append(reserved, "text", "tool_choice")
	}
	body, err = llm.AddExtraFields(body, reserved,
		llm.ExtraFields{Source: "provider extra_payload", Fields: providerExtra},
		llm.ExtraFields{Source: "model extra_payload", Fields: modelExtra},
		llm.ExtraFields{Source: "request ExtraBody", Fields: req.ExtraBody})
	if err != nil {
		return PreparedRequest{}, err
	}
	if err := mergeInclude(body, req.Include); err != nil {
		return PreparedRequest{}, err
	}
	choice, allowed, err := restrictToolChoice(body["tool_choice"], req.AllowedTools)
	if err != nil {
		return PreparedRequest{}, err
	}
	body["tool_choice"] = choice
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err = encoder.Encode(body); err != nil {
		return PreparedRequest{}, fmt.Errorf("marshal response request: %w", err)
	}
	return PreparedRequest{body: append([]byte(nil), buf.Bytes()...), model: req.Model, inputs: len(input), allowedTools: allowed}, nil
}

// StoreForModel exposes the configured preference to the native dialogue loop.
// Per-call Store still takes precedence for independent text and stateless replay.
func (c *Client) StoreForModel(model string) (bool, error) {
	store, source := true, ""
	for _, extra := range []llm.ExtraFields{
		{Source: "provider extra_payload", Fields: c.extraPayload},
		{Source: "model extra_payload", Fields: c.modelExtraPayloads[model]},
	} {
		value, exists := extra.Fields["store"]
		if !exists {
			continue
		}
		if source != "" {
			return false, fmt.Errorf("extra field %q from %s conflicts with %s; extra parameters cannot override existing fields", "store", extra.Source, source)
		}
		configured, ok := value.(bool)
		if !ok {
			return false, fmt.Errorf("store from %s must be a boolean", extra.Source)
		}
		store, source = configured, extra.Source
	}
	return store, nil
}

func withoutStore(fields map[string]any) map[string]any {
	copy := maps.Clone(fields)
	delete(copy, "store")
	return copy
}

// include is additive: route-required recovery material cannot be removed by
// configuration. AddExtraFields still rejects duplicate configuration sources.
func mergeInclude(body map[string]any, required []string) error {
	var configured []string
	if value, ok := body["include"]; ok {
		raw, err := json.Marshal(value)
		if err != nil || len(raw) == 0 || raw[0] != '[' || json.Unmarshal(raw, &configured) != nil {
			return fmt.Errorf("include must be an array of strings")
		}
	}
	seen := map[string]bool{}
	var merged []string
	for _, values := range [][]string{required, configured} {
		for _, value := range values {
			if value == "" {
				return fmt.Errorf("include entries must not be empty")
			}
			if !seen[value] {
				seen[value] = true
				merged = append(merged, value)
			}
		}
	}
	if len(merged) > 0 {
		body["include"] = merged
	}
	return nil
}

func (c *Client) StreamPrepared(ctx context.Context, prepared PreparedRequest) (<-chan Event, error) {
	if len(prepared.body) == 0 {
		return nil, fmt.Errorf("prepared response request is empty")
	}
	if c.logger != nil {
		c.logger.Debug("responses request", "endpoint", c.baseURL+"/responses", "model", prepared.model, "input_items", prepared.inputs, "allowed_tools", len(prepared.allowedTools), "request_bytes", len(prepared.body))
	}
	callCtx, cancel := context.WithCancel(ctx)
	resp, err := c.transport.Do(callCtx, func(ctx context.Context) (*http.Request, error) {
		r, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/responses", bytes.NewReader(prepared.body))
		if err != nil {
			return nil, err
		}
		r.Header.Set("Authorization", "Bearer "+c.apiKey)
		r.Header.Set("Content-Type", "application/json")
		return r, nil
	}, parseError)
	if err != nil {
		cancel()
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		err = parseError(resp)
		_ = resp.Body.Close()
		cancel()
		return nil, err
	}
	sse, err := c.transport.OpenSSE(callCtx, resp)
	if err != nil {
		cancel()
		return nil, err
	}
	events := make(chan Event)
	go func() { defer cancel(); readStream(callCtx, sse, events) }()
	return events, nil
}

func (c *Client) GenerateText(ctx context.Context, req llm.TextRequest) (llm.TextResult, error) {
	input, err := InputMessage("user", []InputContent{{Type: "input_text", Text: req.Input}})
	if err != nil {
		return llm.TextResult{}, err
	}
	store := false
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	events, err := c.Stream(callCtx, Request{Model: req.Model, Instructions: req.Instructions, Input: []Item{input}, Store: &store, MaxOutputTokens: req.MaxOutputTokens, ExtraBody: req.ExtraBody})
	if err != nil {
		return llm.TextResult{}, err
	}
	var result *Response
	for event := range events {
		if event.Error != nil {
			return llm.TextResult{}, event.Error
		}
		if event.Type == "response.completed" {
			result = event.Response
		}
	}
	if err := ctx.Err(); err != nil {
		return llm.TextResult{}, err
	}
	if result == nil {
		return llm.TextResult{}, errors.New("response stream ended without a completed response")
	}
	for _, item := range result.Output {
		if item.Type == "function_call" {
			return llm.TextResult{}, fmt.Errorf("unexpected tool call in independent text generation")
		}
		if item.Type == "message" {
			for _, part := range item.Content {
				if part.Type == "refusal" {
					return llm.TextResult{}, fmt.Errorf("text generation refused: %s", httpclient.SafeSummary([]byte(part.Refusal)))
				}
			}
		}
	}
	return llm.TextResult{Text: result.Text(), Usage: result.TokenUsage()}, nil
}
