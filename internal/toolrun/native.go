package toolrun

import (
	"context"

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

func (s *NativeSource) BaseSchemas(ctx context.Context) []llm.ToolSchema {
	if s == nil || s.Registry == nil {
		return nil
	}
	return s.Registry.SchemasForContext(func(info tool.Info) bool { return AvailableInContext(ctx, info) })
}

func (s *NativeSource) ToolNames(ctx context.Context, actor security.Actor, policy *security.Policy) []string {
	if s == nil || s.Registry == nil {
		return nil
	}
	infos := s.Registry.List()
	names := make([]string, 0, len(infos))
	for _, info := range infos {
		if info.Name == "discover_tool" || info.Hidden || !AvailableInContext(ctx, info) || !tool.CanAccessTool(actor, policy, info) {
			continue
		}
		names = append(names, info.Name)
	}
	return names
}
