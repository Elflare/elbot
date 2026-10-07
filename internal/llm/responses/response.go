package responses

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"elbot/internal/llm"
)

// Item retains the entire native object. Decoded fields are read-only views;
// marshaling uses Raw so encrypted reasoning and unknown fields survive replay.
type Item struct {
	Raw       json.RawMessage `json:"-"`
	Type      string          `json:"type"`
	ID        string          `json:"id,omitempty"`
	Status    string          `json:"status,omitempty"`
	Role      string          `json:"role,omitempty"`
	CallID    string          `json:"call_id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments string          `json:"arguments,omitempty"`
	Content   []OutputContent `json:"content,omitempty"`
}

type OutputContent struct {
	Type    string `json:"type"`
	Text    string `json:"text,omitempty"`
	Refusal string `json:"refusal,omitempty"`
}

func (c *OutputContent) UnmarshalJSON(raw []byte) error {
	var header struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		return err
	}
	*c = OutputContent{Type: header.Type}
	// Opaque/unknown parts may have fields named text with different shapes.
	if header.Type == "output_text" || header.Type == "input_text" || header.Type == "refusal" {
		type view OutputContent
		var parsed view
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return err
		}
		*c = OutputContent(parsed)
	}
	return nil
}

func ParseItem(raw []byte) (Item, error) {
	var item Item
	err := json.Unmarshal(raw, &item)
	return item, err
}

func (i *Item) UnmarshalJSON(raw []byte) error {
	if !json.Valid(raw) || !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		return fmt.Errorf("response item must be a JSON object")
	}
	var header struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		return err
	}
	if header.Type == "" {
		return fmt.Errorf("response item requires type")
	}
	*i = Item{Type: header.Type, ID: header.ID}
	// Unknown item shapes remain opaque, including fields named content.
	if header.Type == "message" || header.Type == "function_call" {
		type view Item
		var parsed view
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return err
		}
		*i = Item(parsed)
	}
	i.Raw = append(json.RawMessage(nil), raw...)
	return nil
}

func (i Item) MarshalJSON() ([]byte, error) {
	if !json.Valid(i.Raw) || !bytes.HasPrefix(bytes.TrimSpace(i.Raw), []byte("{")) {
		return nil, fmt.Errorf("response item requires its native JSON object")
	}
	return i.Raw, nil
}

type Response struct {
	Raw               json.RawMessage    `json:"-"`
	ID                string             `json:"id"`
	Status            string             `json:"status"`
	Store             *bool              `json:"store,omitempty"`
	Model             string             `json:"model"`
	Output            []Item             `json:"output"`
	Usage             *ResponseUsage     `json:"usage"`
	Error             *APIError          `json:"error"`
	IncompleteDetails *IncompleteDetails `json:"incomplete_details"`
}

type APIError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Param      string `json:"param"`
	StatusCode int    `json:"-"`
}
type IncompleteDetails struct {
	Reason string `json:"reason"`
}
type ResponseUsage struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	TotalTokens        int `json:"total_tokens"`
	InputTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
}

func (r *Response) UnmarshalJSON(raw []byte) error {
	if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		return fmt.Errorf("response must be a JSON object")
	}
	type view Response
	var parsed view
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return err
	}
	*r = Response(parsed)
	r.Raw = append(json.RawMessage(nil), raw...)
	return nil
}

func (r Response) MarshalJSON() ([]byte, error) {
	if !json.Valid(r.Raw) {
		return nil, fmt.Errorf("response requires its native JSON")
	}
	return r.Raw, nil
}

func (r *Response) Text() string {
	var text strings.Builder
	for _, item := range r.Output {
		if item.Type == "message" {
			for _, part := range item.Content {
				if part.Type == "output_text" {
					text.WriteString(part.Text)
				}
			}
		}
	}
	return text.String()
}

func (r *Response) TokenUsage() *llm.Usage {
	if r.Usage == nil {
		return nil
	}
	u := r.Usage
	return &llm.Usage{PromptTokens: u.InputTokens, CompletionTokens: u.OutputTokens, TotalTokens: u.TotalTokens, CacheHitTokens: u.InputTokensDetails.CachedTokens}
}
