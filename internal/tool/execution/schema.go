// 组合基础工具与持久化的已发现声明，资格判断复用 Registry。
package execution

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	"elbot/internal/storage"
	"elbot/internal/tool/registry"
	"elbot/internal/tool/share"
)

func (m *Manager) Schemas(ctx context.Context, view share.Context, discovered []share.DiscoveredTool) ([]llm.ToolSchema, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if view.Mode != storage.SessionModeWork && view.Mode != storage.SessionModeBackground {
		return nil, nil
	}
	if view.Actor.Role != "" {
		ctx = contextinfo.WithActor(ctx, view.Actor)
	}
	var out []llm.ToolSchema
	seen := map[string]bool{}
	appendSchema := func(schema llm.ToolSchema) {
		if schema.Name == "" || seen[schema.Name] {
			return
		}
		seen[schema.Name] = true
		out = append(out, share.CloneSchema(schema))
	}
	if view.Mode == storage.SessionModeWork && !view.DisableBaseTools {
		tools, err := m.registry.QueryTools(ctx, share.ToolQuery{Names: []string{"discover_tool"}, IncludeDiscover: true})
		if err != nil {
			return nil, err
		}
		for _, candidate := range tools {
			appendSchema(candidate.Schema())
		}
	}
	if view.Mode == storage.SessionModeBackground {
		discovered = registry.BackgroundDiscoveredTools(ctx, discovered)
	}
	for _, item := range discovered {
		if view.Mode != storage.SessionModeBackground && !registry.InfoAvailableInContext(ctx, share.Info{ForegroundOnly: item.ForegroundOnly}) {
			continue
		}
		appendSchema(item.Schema)
	}
	return out, nil
}
