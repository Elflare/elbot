package responses

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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

func (p PreparedCompactRequest) JSON() json.RawMessage { return p.request.JSON() }

// Response retains the real terminal response and all completed output for audit.
// Only Compaction becomes the next session's context root.
type CompactResult struct {
	*Response
	Compaction Item
}

func (c *Client) PrepareCompact(req CompactRequest) (PreparedCompactRequest, error) {
	input := make([]Item, 0, len(req.Input)+1)
	for _, item := range req.Input {
		if item.Type == "compaction_trigger" {
			return PreparedCompactRequest{}, fmt.Errorf("compaction_trigger must not appear in retained context")
		}
		input = append(input, item)
	}
	trigger, err := ParseItem([]byte(`{"type":"compaction_trigger"}`))
	if err != nil {
		return PreparedCompactRequest{}, err
	}
	input = append(input, trigger)
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
	return c.CompactPrepared(ctx, prepared)
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
	completed := false
	for event := range events {
		if event.Response != nil {
			result.Response = event.Response
		}
		if event.Error != nil {
			if len(result.Output) == 0 {
				result.Output = completedItems
			}
			return result, event.Error
		}
		switch event.Type {
		case "response.output_item.done":
			if event.Item == nil {
				return result, fmt.Errorf("compact output_item.done is missing item")
			}
			completedItems = append(completedItems, *event.Item)
		case "response.completed":
			completed = true
		}
	}
	if len(result.Output) == 0 {
		result.Output = completedItems
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if !completed {
		return result, fmt.Errorf("compact stream closed before response.completed")
	}
	// A terminal output array repeats output_item.done; it is not another set of
	// items. Validate each representation separately rather than counting twice.
	if len(completedItems) > 0 {
		if _, err := compactOutput(completedItems); err != nil {
			return result, err
		}
	}
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
