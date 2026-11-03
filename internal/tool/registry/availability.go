// 集中处理工具的上下文可用性与访问权限判断。
package registry

import (
	"context"
	"slices"
	"sort"

	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/tool/share"
)

func InfoAvailableInContext(ctx context.Context, info share.Info) bool {
	return UnavailableReason(ctx, info) == ""
}

func UnavailableReason(ctx context.Context, info share.Info) string {
	if info.ForegroundOnly && share.PolicyFromContext(ctx).DisableForegroundTools {
		return "tool is only available in foreground sessions"
	}
	if len(info.APITypes) > 0 {
		model, ok := contextinfo.ModelFromContext(ctx)
		if !ok || model.APIType == "" || !slices.Contains(info.APITypes, llm.APIType(model.APIType)) {
			return "tool is unavailable for the current API type"
		}
	}
	return ""
}

func CanAccessTool(actor contextinfo.Actor, permission interface {
	CanUseTool(contextinfo.Actor, security.RiskLevel, bool) bool
}, info share.Info) bool {
	if info.SuperadminOnly && actor.Role != contextinfo.RoleSuperadmin {
		return false
	}
	// 没有风险声明的条目只能是纯文档；仍遵守上面的可见性限制。
	return info.Risk == "" || permission.CanUseTool(actor, info.Risk, info.OwnerScoped)
}

// Query 统一筛选目录；只分配结果容器，定义中的 map、slice 只读共享。
func (r *Registry) Query(ctx context.Context, query share.ToolQuery) ([]share.ToolDefinition, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	actor, _ := contextinfo.ActorFromContext(ctx)
	r.mu.RLock()
	var candidates []share.ToolDefinition
	if query.Names == nil {
		candidates = make([]share.ToolDefinition, 0, len(r.tools))
		for _, candidate := range r.tools {
			candidates = append(candidates, candidate)
		}
	} else {
		candidates = make([]share.ToolDefinition, 0, len(query.Names))
		seen := make(map[string]bool, len(query.Names))
		for _, name := range query.Names {
			if seen[name] {
				continue
			}
			seen[name] = true
			if candidate, ok := r.tools[name]; ok {
				candidates = append(candidates, candidate)
			}
		}
	}
	r.mu.RUnlock()
	tools := candidates[:0]
	for _, candidate := range candidates {
		info := candidate.Definition
		if (!query.IncludeHidden && info.Hidden) || (!query.IncludeDiscover && info.Name == "discover_tool") {
			continue
		}
		skill := candidate.LoadDetail != nil
		if (query.Kind == "tool" && skill) || (query.Kind == "skill" && !skill) {
			continue
		}
		if query.Scope == share.QueryAvailable && (!InfoAvailableInContext(ctx, info) || !CanAccessTool(actor, r.permission, info)) {
			continue
		}
		tools = append(tools, candidate)
	}
	clear(candidates[len(tools):])
	sort.Slice(tools, func(i, j int) bool { return tools[i].Definition.Name < tools[j].Definition.Name })
	return tools, nil
}

// BackgroundDiscoveredTools applies background restrictions to executable discovery metadata.
func BackgroundDiscoveredTools(items []share.DiscoveredTool) []share.DiscoveredTool {
	var result []share.DiscoveredTool
	for _, item := range items {
		if item.ForegroundOnly {
			continue
		}
		allowed := true
		for _, name := range []string{item.Name, item.CanonicalName, item.Schema.Name} {
			if name == "discover_tool" || name == "workspace" {
				allowed = false
				break
			}
		}
		if allowed {
			result = append(result, item)
		}
	}
	return result
}
