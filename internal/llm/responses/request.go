package responses

import (
	"encoding/json"
	"fmt"

	"elbot/internal/llm"
)

// Request owns Responses context and the currently callable function names.
// Definitions enter the context through AdditionalTools input items.
type Request struct {
	Model              string
	Instructions       string
	Input              []Item
	AllowedTools       []string
	PreviousResponseID string
	Store              *bool
	MaxOutputTokens    int
	Temperature        *float64
	Include            []string
	ExtraBody          map[string]any
}

// FunctionTool only represents ElBot functions, never hosted tools.
type FunctionTool struct {
	Name        string
	Description string
	Parameters  map[string]any
	Strict      bool
}

func (t FunctionTool) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type        string         `json:"type"`
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
		Strict      bool           `json:"strict"`
	}{"function", t.Name, t.Description, t.Parameters, t.Strict})
}

func FunctionTools(schemas []llm.ToolSchema) []FunctionTool {
	tools := make([]FunctionTool, 0, len(schemas))
	for _, s := range schemas {
		tools = append(tools, FunctionTool{Name: s.Name, Description: s.Description, Parameters: s.Parameters})
	}
	return tools
}

// InputContent uses Responses' own image/file/text field names.
type InputContent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	Detail   string `json:"detail,omitempty"`
	FileID   string `json:"file_id,omitempty"`
	FileURL  string `json:"file_url,omitempty"`
	FileData string `json:"file_data,omitempty"`
	Filename string `json:"filename,omitempty"`
}

func InputMessage(role string, content []InputContent) (Item, error) {
	raw, err := json.Marshal(struct {
		Type    string         `json:"type"`
		Role    string         `json:"role"`
		Content []InputContent `json:"content"`
	}{"message", role, content})
	if err != nil {
		return Item{}, err
	}
	return ParseItem(raw)
}

func FunctionCallOutput(callID string, output json.RawMessage) (Item, error) {
	if callID == "" {
		return Item{}, fmt.Errorf("function output requires call_id")
	}
	raw, err := json.Marshal(struct {
		Type   string          `json:"type"`
		CallID string          `json:"call_id"`
		Output json.RawMessage `json:"output"`
	}{"function_call_output", callID, output})
	if err != nil {
		return Item{}, err
	}
	return ParseItem(raw)
}
