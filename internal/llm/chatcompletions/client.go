package chatcompletions

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"elbot/internal/llm"
	"elbot/internal/llm/httpclient"
)

var _ Streamer = (*Client)(nil)

type RequestOptions = httpclient.Options

type Streamer interface {
	llm.Client
	Stream(context.Context, Request) (<-chan Chunk, error)
}

type Client struct {
	baseURL            string
	apiKey             string
	extraPayload       map[string]any
	modelExtraPayloads map[string]map[string]any
	transport          *httpclient.Client

	loggedSystemMu sync.Mutex
	loggedSystem   map[string]bool
}

func New(baseURL, apiKey string, extraPayload map[string]any, modelExtraPayloads map[string]map[string]any, opts RequestOptions) (*Client, error) {
	transport, err := httpclient.New(opts)
	if err != nil {
		return nil, err
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, extraPayload: extraPayload, modelExtraPayloads: modelExtraPayloads, transport: transport, loggedSystem: map[string]bool{}}, nil
}

func (a *Client) SetRetryNotifier(f func(context.Context, llm.RetryEvent)) {
	a.transport.SetRetryNotifier(f)
}
func (a *Client) endpoint() string { return a.baseURL + "/chat/completions" }
func (a *Client) validateBaseURL() error {
	if strings.TrimSpace(a.baseURL) == "" {
		return errors.New("chat base_url is required")
	}
	return nil
}

func (a *Client) Stream(ctx context.Context, req Request) (<-chan Chunk, error) {
	if err := a.validateBaseURL(); err != nil {
		return nil, err
	}
	body := map[string]any{"model": req.Model, "messages": toOpenAIMessages(req.Messages), "stream": true, "stream_options": map[string]any{"include_usage": true}}
	if req.Temperature > 0 {
		body["temperature"] = req.Temperature
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if len(req.Tools) > 0 {
		body["tools"] = toOpenAITools(req.Tools)
	}
	body, err := llm.AddExtraFields(body, []string{"model", "messages", "tools", "stream", "stream_options"},
		llm.ExtraFields{Source: "provider extra_payload", Fields: a.extraPayload},
		llm.ExtraFields{Source: "model extra_payload", Fields: a.modelExtraPayloads[req.Model]},
		llm.ExtraFields{Source: "request ExtraBody", Fields: req.ExtraBody})
	if err != nil {
		return nil, err
	}
	bodyBytes, err := marshalJSONNoEscape(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	a.logChatRequest(ctx, req, bodyBytes)
	responseCtx, cancel := context.WithCancel(ctx)
	resp, err := a.transport.Do(responseCtx, func(ctx context.Context) (*http.Request, error) {
		r, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint(), bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, err
		}
		r.Header.Set("Authorization", "Bearer "+a.apiKey)
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
	sse, err := a.transport.OpenSSE(responseCtx, resp)
	if err != nil {
		cancel()
		return nil, err
	}
	out := make(chan Chunk)
	go func() { defer cancel(); a.readStream(responseCtx, sse, out) }()
	return out, nil
}

func (a *Client) GenerateText(ctx context.Context, req llm.TextRequest) (llm.TextResult, error) {
	messages := []llm.LLMMessage{}
	if req.Instructions != "" {
		messages = append(messages, llm.LLMMessage{Role: llm.RoleSystem, Segments: llm.TextSegments(req.Instructions)})
	}
	messages = append(messages, llm.LLMMessage{Role: llm.RoleUser, Segments: llm.TextSegments(req.Input)})
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	chunks, err := a.Stream(callCtx, Request{Model: req.Model, Messages: messages, MaxTokens: req.MaxOutputTokens, ExtraBody: req.ExtraBody})
	if err != nil {
		return llm.TextResult{}, err
	}
	var text strings.Builder
	var usage *llm.Usage
	for chunk := range chunks {
		if chunk.Error != nil {
			return llm.TextResult{}, chunk.Error
		}
		if len(chunk.ToolCallDeltas) > 0 {
			return llm.TextResult{}, errors.New("unexpected tool call in independent text generation")
		}
		if chunk.FinishReason == "length" || chunk.FinishReason == "content_filter" {
			return llm.TextResult{}, fmt.Errorf("text generation incomplete: %s", chunk.FinishReason)
		}
		text.WriteString(chunk.DeltaContent)
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
	}
	if err := ctx.Err(); err != nil {
		return llm.TextResult{}, err
	}
	return llm.TextResult{Text: text.String(), Usage: usage}, nil
}
