// 适配原生工具来源，提供执行所需的工具查询。
package execution

import toolshare "elbot/internal/tool/share"

import (
	"context"

	"elbot/internal/contextinfo"
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

func (s *NativeSource) Get(name string) (toolshare.Tool, bool) {
	return s.Registry.Get(name)
}

func (s *NativeSource) BaseSchemas(ctx context.Context) []llm.ToolSchema {
	return s.Registry.SchemasForContext(func(info toolshare.Info) bool { return AvailableInContext(ctx, info) })
}

func (s *NativeSource) ToolInfos(ctx context.Context, actor contextinfo.Actor, permission *security.Permission) []toolshare.Info {
	infos := s.Registry.List()
	available := make([]toolshare.Info, 0, len(infos))
	for _, info := range infos {
		if info.Name == "discover_tool" || info.Hidden || !AvailableInContext(ctx, info) || !tool.CanAccessTool(actor, permission, info) {
			continue
		}
		available = append(available, info)
	}
	return available
}

func (s *NativeSource) ToolNames(ctx context.Context, actor contextinfo.Actor, permission *security.Permission) []string {
	infos := s.ToolInfos(ctx, actor, permission)
	names := make([]string, 0, len(infos))
	for _, info := range infos {
		names = append(names, info.Name)
	}
	return names
}
