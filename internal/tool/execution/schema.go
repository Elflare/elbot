// 组合基础工具与持久化的已发现声明，资格判断复用 Registry。
package execution

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	"elbot/internal/storage"
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
		tools, err := m.Registry.QueryTools(ctx, share.ToolQuery{Names: []string{"discover_tool"}, IncludeDiscover: true})
		if err != nil {
			return nil, err
		}
		for _, candidate := range tools {
			appendSchema(candidate.Schema())
		}
	}
	if view.Mode == storage.SessionModeBackground {
		discovered = m.BackgroundDiscoveredTools(ctx, discovered)
	}
	for _, item := range discovered {
		if view.Mode != storage.SessionModeBackground && !m.InfoAvailableInContext(ctx, share.Info{ForegroundOnly: item.ForegroundOnly}) {
			continue
		}
		appendSchema(item.Schema)
	}
	return out, nil
}

// FilterSchemas 复核 Hook 修改后的声明，保留未知工具及调用方提供的 schema 和顺序。
func (m *Manager) FilterSchemas(ctx context.Context, schemas []llm.ToolSchema) ([]llm.ToolSchema, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if schemas == nil {
		return nil, nil
	}
	out := make([]llm.ToolSchema, 0, len(schemas))
	for _, schema := range schemas {
		if info, ok := m.Registry.ToolInfo(schema.Name); ok && !m.InfoAvailableInContext(ctx, info) {
			continue
		}
		out = append(out, schema)
	}
	return out, nil
}
