package llm

// ToolCallRequest is the business view of a requested function call.
type ToolCallRequest struct {
	ID        string
	Name      string
	Arguments string
}

// ToolSchema describes an ElBot function without a protocol's JSON wrapper.
type ToolSchema struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}
