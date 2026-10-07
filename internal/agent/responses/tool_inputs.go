package responses

import (
	"context"
	"encoding/json"
	"fmt"

	api "elbot/internal/llm/responses"
	"elbot/internal/storage"
)

// toolDefinitions is a disposable index of definitions in the native window and
// its durable pending inputs. Session tool availability remains owned by ToolRun.
type toolDefinitions map[string]string

func (d toolDefinitions) add(tools []api.FunctionTool) error {
	for _, tool := range tools {
		raw, err := json.Marshal(tool)
		if err != nil {
			return err
		}
		if previous, exists := d[tool.Name]; exists && previous != string(raw) {
			return fmt.Errorf("Responses 工具 %q 的定义已变化，请新建会话", tool.Name)
		}
		d[tool.Name] = string(raw)
	}
	return nil
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
		if err := definitions.add(tools); err != nil {
			return nil, err
		}
	}
	return definitions, nil
}

func (s *turnState) queueToolDefinitions(ctx context.Context, inputs []storage.NativeInput) ([]storage.NativeInput, error) {
	known := toolDefinitions{}
	for name, definition := range s.definitions {
		known[name] = definition
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
			if err := known.add(tools); err != nil {
				return nil, err
			}
		}
	}
	var added []api.FunctionTool
	for _, tool := range api.FunctionTools(s.tools) {
		_, exists := known[tool.Name]
		if err := known.add([]api.FunctionTool{tool}); err != nil {
			return nil, err
		}
		if !exists {
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
