package responses

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// AdditionalTools freezes function definitions at their position in the input.
// Tool results remain separate function_call_output items.
func AdditionalTools(tools []FunctionTool) (Item, error) {
	raw, err := json.Marshal(struct {
		Type  string         `json:"type"`
		Role  string         `json:"role"`
		Tools []FunctionTool `json:"tools"`
	}{"additional_tools", "developer", tools})
	if err != nil {
		return Item{}, err
	}
	item, err := ParseItem(raw)
	if err == nil {
		_, err = AdditionalToolDefinitions(item)
	}
	return item, err
}

// AdditionalToolDefinitions reads the ElBot function definitions retained in a
// native item. UseNumber preserves integer schemas across durable round trips.
func AdditionalToolDefinitions(item Item) ([]FunctionTool, error) {
	var header struct {
		Type  string            `json:"type"`
		Role  string            `json:"role"`
		Tools []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(item.Raw, &header); err != nil {
		return nil, err
	}
	if header.Type != "additional_tools" || header.Role != "developer" || len(header.Tools) == 0 {
		return nil, fmt.Errorf("additional_tools requires developer role and non-empty function definitions")
	}
	tools := make([]FunctionTool, 0, len(header.Tools))
	seen := map[string]bool{}
	for _, raw := range header.Tools {
		var kind struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &kind); err != nil {
			return nil, err
		}
		var tool FunctionTool
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&tool); err != nil {
			return nil, err
		}
		if kind.Type != "function" || strings.TrimSpace(tool.Name) == "" || seen[tool.Name] {
			return nil, fmt.Errorf("invalid or duplicate additional function %q", tool.Name)
		}
		seen[tool.Name] = true
		tools = append(tools, tool)
	}
	return tools, nil
}

type functionChoice struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

type allowedToolChoice struct {
	Type  string           `json:"type"`
	Mode  string           `json:"mode"`
	Tools []functionChoice `json:"tools"`
}

// restrictToolChoice intersects optional caller configuration with the route's
// current tool permissions. Historical definitions alone never enable a tool.
func restrictToolChoice(configured any, names []string) (any, []string, error) {
	allowed := make(map[string]bool, len(names))
	for _, name := range names {
		if strings.TrimSpace(name) == "" {
			return nil, nil, fmt.Errorf("allowed function name is empty")
		}
		allowed[name] = true
	}
	mode := "auto"
	if configured != nil {
		raw, err := json.Marshal(configured)
		if err != nil {
			return nil, nil, fmt.Errorf("encode tool_choice: %w", err)
		}
		var text string
		if json.Unmarshal(raw, &text) == nil {
			switch text {
			case "none":
				return "none", nil, nil
			case "auto", "required":
				mode = text
			default:
				return nil, nil, fmt.Errorf("unsupported tool_choice %q", text)
			}
		} else {
			var choice struct {
				Type  string           `json:"type"`
				Mode  string           `json:"mode"`
				Name  string           `json:"name"`
				Tools []functionChoice `json:"tools"`
			}
			if err := json.Unmarshal(raw, &choice); err != nil {
				return nil, nil, fmt.Errorf("decode tool_choice: %w", err)
			}
			switch choice.Type {
			case "function":
				if !allowed[choice.Name] {
					return nil, nil, fmt.Errorf("tool_choice forces unavailable function %q", choice.Name)
				}
				return functionChoice{Type: "function", Name: choice.Name}, []string{choice.Name}, nil
			case "allowed_tools":
				if choice.Mode != "auto" && choice.Mode != "required" {
					return nil, nil, fmt.Errorf("invalid allowed_tools mode %q", choice.Mode)
				}
				mode = choice.Mode
				subset := make(map[string]bool, len(choice.Tools))
				for _, tool := range choice.Tools {
					if tool.Type != "function" || strings.TrimSpace(tool.Name) == "" {
						return nil, nil, fmt.Errorf("tool_choice only supports named ElBot functions")
					}
					if allowed[tool.Name] {
						subset[tool.Name] = true
					}
				}
				allowed = subset
			default:
				return nil, nil, fmt.Errorf("unsupported tool_choice type %q", choice.Type)
			}
		}
	}
	if len(allowed) == 0 {
		if mode == "required" {
			return nil, nil, fmt.Errorf("tool_choice requires a function but no functions are allowed")
		}
		return "none", nil, nil
	}
	ordered := make([]string, 0, len(allowed))
	for name := range allowed {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	choice := allowedToolChoice{Type: "allowed_tools", Mode: mode}
	for _, name := range ordered {
		choice.Tools = append(choice.Tools, functionChoice{Type: "function", Name: name})
	}
	return choice, ordered, nil
}
