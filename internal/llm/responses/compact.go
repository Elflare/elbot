package responses

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"elbot/internal/llm"
)

type CompactRequest struct {
	Model, Instructions string
	Input               []Item
	ExtraBody           map[string]any
}

type Compactor interface {
	PrepareCompact(CompactRequest) (PreparedCompactRequest, error)
	CompactPrepared(context.Context, PreparedCompactRequest) (*CompactResult, error)
}

type PreparedCompactRequest struct{ body []byte }

func (p PreparedCompactRequest) JSON() json.RawMessage {
	return append(json.RawMessage(nil), p.body...)
}

type CompactResult struct {
	Raw    json.RawMessage `json:"-"`
	ID     string          `json:"id"`
	Object string          `json:"object"`
	Output []Item          `json:"output"`
	Usage  *ResponseUsage  `json:"usage"`
}

func (r *CompactResult) TokenUsage() *llm.Usage {
	return (&Response{Usage: r.Usage}).TokenUsage()
}

func (c *Client) PrepareCompact(req CompactRequest) (PreparedCompactRequest, error) {
	if err := c.validateBaseURL(); err != nil {
		return PreparedCompactRequest{}, err
	}
	input := req.Input
	if input == nil {
		input = []Item{}
	}
	body := map[string]any{"model": req.Model, "input": input}
	if req.Instructions != "" {
		body["instructions"] = req.Instructions
	}
	// Generation options (reasoning, text.format, tools, streaming) do not belong
	// to /responses/compact. Only its documented cache/service options carry over.
	compactExtras := func(source map[string]any) map[string]any {
		extra := make(map[string]any)
		for _, key := range []string{"service_tier", "prompt_cache_key", "prompt_cache_options", "prompt_cache_retention"} {
			if value, ok := source[key]; ok {
				extra[key] = value
			}
		}
		return extra
	}
	body, err := llm.AddExtraFields(body, []string{"model", "input", "instructions", "previous_response_id", "conversation", "store", "stream", "tools"},
		llm.ExtraFields{Source: "provider compact options", Fields: compactExtras(c.extraPayload)},
		llm.ExtraFields{Source: "model compact options", Fields: compactExtras(c.modelExtraPayloads[req.Model])},
		llm.ExtraFields{Source: "compact ExtraBody", Fields: req.ExtraBody})
	if err != nil {
		return PreparedCompactRequest{}, err
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(body); err != nil {
		return PreparedCompactRequest{}, err
	}
	return PreparedCompactRequest{body: append([]byte(nil), buf.Bytes()...)}, nil
}

func (c *Client) Compact(ctx context.Context, req CompactRequest) (*CompactResult, error) {
	prepared, err := c.PrepareCompact(req)
	if err != nil {
		return nil, err
	}
	return c.CompactPrepared(ctx, prepared)
}

func (c *Client) CompactPrepared(ctx context.Context, prepared PreparedCompactRequest) (*CompactResult, error) {
	if len(prepared.body) == 0 {
		return nil, fmt.Errorf("prepared compact request is empty")
	}
	resp, err := c.transport.Do(ctx, func(ctx context.Context) (*http.Request, error) {
		r, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/responses/compact", bytes.NewReader(prepared.body))
		if err != nil {
			return nil, err
		}
		r.Header.Set("Authorization", "Bearer "+c.apiKey)
		r.Header.Set("Content-Type", "application/json")
		return r, nil
	}, parseError)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, parseError(resp)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var result CompactResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("decode compact result: %w", err)
	}
	if result.ID == "" || result.Object != "response.compaction" || len(result.Output) == 0 {
		return nil, fmt.Errorf("incomplete compact result")
	}
	result.Raw = append(json.RawMessage(nil), raw...)
	return &result, nil
}
