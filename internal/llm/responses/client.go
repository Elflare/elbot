package responses

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"elbot/internal/llm"
	"elbot/internal/llm/httpclient"
)

var _ Streamer = (*Client)(nil)

type RequestOptions = httpclient.Options

type Streamer interface {
	llm.Client
	Stream(context.Context, Request) (<-chan Event, error)
}

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
	if strings.TrimSpace(c.baseURL) == "" {
		return nil, errors.New("response base_url is required")
	}
	input := req.Input
	if input == nil {
		input = []Item{}
	}
	body := map[string]any{"model": req.Model, "input": input, "stream": true}
	if req.Instructions != "" {
		body["instructions"] = req.Instructions
	}
	if len(req.Tools) > 0 {
		body["tools"] = req.Tools
	}
	if req.PreviousResponseID != "" {
		body["previous_response_id"] = req.PreviousResponseID
	}
	if req.Store != nil {
		body["store"] = *req.Store
	}
	if req.MaxOutputTokens > 0 {
		body["max_output_tokens"] = req.MaxOutputTokens
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	body, err := llm.AddExtraFields(body, []string{"model", "input", "instructions", "tools", "stream", "previous_response_id", "conversation", "store"},
		llm.ExtraFields{Source: "provider extra_payload", Fields: c.extraPayload},
		llm.ExtraFields{Source: "model extra_payload", Fields: c.modelExtraPayloads[req.Model]},
		llm.ExtraFields{Source: "request ExtraBody", Fields: req.ExtraBody})
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err = encoder.Encode(body); err != nil {
		return nil, fmt.Errorf("marshal response request: %w", err)
	}
	if c.logger != nil {
		c.logger.Debug("responses request", "endpoint", c.baseURL+"/responses", "model", req.Model, "input_items", len(input), "tools", len(req.Tools), "request_bytes", buf.Len())
	}
	callCtx, cancel := context.WithCancel(ctx)
	resp, err := c.transport.Do(callCtx, func(ctx context.Context) (*http.Request, error) {
		r, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/responses", bytes.NewReader(buf.Bytes()))
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
