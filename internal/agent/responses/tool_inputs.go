package responses

import (
	"context"

	api "elbot/internal/llm/responses"
	"elbot/internal/storage"
)

// toolDefinitions indexes tool names in the native window and
// its durable pending inputs. Session tool availability remains owned by ToolRun.
type toolDefinitions map[string]bool

func (d toolDefinitions) add(tools []api.FunctionTool) {
	for _, tool := range tools {
		d[tool.Name] = true
	}
}

func definitionsIn(items []api.Item) (toolDefinitions, error) {
	definitions := toolDefinitions{}
	for _, item := range items {
		if item.Type != "additional_tools" {
			continue
		}
		tools, err := api.AdditionalToolDefinitions(item)
		if err != nil {
			return nil, err
		}
		definitions.add(tools)
	}
	return definitions, nil
}

func (s *turnState) queueToolDefinitions(ctx context.Context, inputs []storage.NativeInput) ([]storage.NativeInput, error) {
	known := toolDefinitions{}
	for name := range s.definitions {
		known[name] = true
	}
	for _, input := range inputs {
		item, _, err := decodeQueuedInput(input)
		if err != nil {
			return nil, err
		}
		if item.Type == "additional_tools" {
			tools, err := api.AdditionalToolDefinitions(item)
			if err != nil {
				return nil, err
			}
			known.add(tools)
		}
	}
	var added []api.FunctionTool
	for _, tool := range api.FunctionTools(s.tools) {
		if !known[tool.Name] {
			known[tool.Name] = true
			added = append(added, tool)
		}
	}
	if len(added) > 0 {
		item, err := api.AdditionalTools(added)
		if err != nil {
			return nil, err
		}
		input := storage.NativeInput{ID: storage.NewID(), SessionID: s.session.ID, ItemJSON: string(item.Raw), MediaJSON: "[]"}
		native := storage.NativeCommit{ExpectedCheckpointID: s.checkpointID(), Inputs: []storage.NativeInput{input}}
		if err := s.commit(ctx, storage.DialogueCommit{SessionID: s.session.ID, Native: &native}, "append_tool_definitions"); err != nil {
			return nil, err
		}
		inputs = append(inputs, input)
	}
	// Queued definitions count for deduplication, but only advance() consumes them.
	s.definitions = known
	return inputs, nil
}

func (s *turnState) allowedToolNames() []string {
	names := make([]string, 0, len(s.tools))
	for _, tool := range s.tools {
		names = append(names, tool.Name)
	}
	return names
}
