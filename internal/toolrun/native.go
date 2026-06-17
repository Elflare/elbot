package toolrun

import (
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/tool"
)

type NativeSource struct {
	Registry *tool.Registry
}

func NewNativeSource(registry *tool.Registry) *NativeSource {
	return &NativeSource{Registry: registry}
}

func (s *NativeSource) Get(name string) (tool.Tool, bool) {
	if s == nil || s.Registry == nil {
		return nil, false
	}
	return s.Registry.Get(name)
}

func (s *NativeSource) BaseSchemas() []llm.ToolSchema {
	if s == nil || s.Registry == nil {
		return nil
	}
	return s.Registry.Schemas()
}

func (s *NativeSource) ToolNames(actor security.Actor, policy *security.Policy) []string {
	if s == nil || s.Registry == nil {
		return nil
	}
	infos := s.Registry.List()
	names := make([]string, 0, len(infos))
	for _, info := range infos {
		if info.Name == "discover_tool" || info.Hidden || !tool.CanAccessTool(actor, policy, info) {
			continue
		}
		names = append(names, info.Name)
	}
	return names
}
