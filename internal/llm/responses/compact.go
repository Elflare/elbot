package responses

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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

type PreparedCompactRequest struct{ request PreparedRequest }

func (p PreparedCompactRequest) JSON() json.RawMessage           { return p.request.JSON() }
func (p PreparedCompactRequest) ParametersJSON() json.RawMessage { return p.request.ParametersJSON() }

// Response retains the real terminal response and all completed output for audit.
// Only Compaction becomes the next session's context root.
type CompactResult struct {
	*Response
	Compaction Item
}

func (c *Client) PrepareCompact(req CompactRequest) (PreparedCompactRequest, error) {
	input := make([]Item, 0, len(req.Input)+1)
	input = append(input, req.Input...)
	input = append(input, Item{Type: "compaction_trigger", Raw: json.RawMessage(`{"type":"compaction_trigger"}`)})
	store := false
	prepared, err := c.prepareRequest(Request{
		Model: req.Model, Instructions: req.Instructions, Input: input,
		Store: &store, Include: []string{"reasoning.encrypted_content"}, ExtraBody: req.ExtraBody,
	}, true)
	return PreparedCompactRequest{request: prepared}, err
}

func (c *Client) Compact(ctx context.Context, req CompactRequest) (*CompactResult, error) {
	prepared, err := c.PrepareCompact(req)
	if err != nil {
		return nil, err
	}
	return llm.Retry(ctx, c, c.CompactPrepared, prepared)
}

func (c *Client) CompactPrepared(ctx context.Context, prepared PreparedCompactRequest) (*CompactResult, error) {
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	events, err := c.StreamPrepared(callCtx, prepared.request)
	if err != nil {
		return nil, err
	}
	result := &CompactResult{Response: &Response{}}
	var completedItems []Item
	for event := range events {
		if event.Response != nil {
			result.Response = event.Response
		}
		if event.Error != nil {
			err = event.Error
			break
		}
		if event.Type == "response.output_item.done" && event.Item != nil {
			completedItems = append(completedItems, *event.Item)
		}
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		if len(result.Output) == 0 {
			result.Output = completedItems
		}
		return result, err
	}
	// Only the terminal response can supply the next context root. Intermediate
	// done items are retained solely for auditing failed or canceled attempts.
	result.Compaction, err = compactOutput(result.Output)
	return result, err
}

func compactOutput(items []Item) (Item, error) {
	var compact Item
	count := 0
	for _, item := range items {
		if item.Type == "compaction" {
			count++
			compact = item
		}
	}
	if count != 1 {
		return Item{}, fmt.Errorf("compact expected exactly one compaction item, got %d", count)
	}
	var payload struct {
		EncryptedContent string `json:"encrypted_content"`
	}
	if err := json.Unmarshal(compact.Raw, &payload); err != nil || strings.TrimSpace(payload.EncryptedContent) == "" {
		return Item{}, fmt.Errorf("compaction item requires non-empty encrypted_content")
	}
	return compact, nil
}
