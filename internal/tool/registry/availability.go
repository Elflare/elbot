// 集中处理工具的上下文可用性与访问权限判断。
package registry

import (
	"context"
	"slices"
	"sort"

	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	sandboxctx "elbot/internal/sandbox"
	"elbot/internal/security"
	"elbot/internal/tool/share"
)

func InfoAvailableInContext(ctx context.Context, info share.Info) bool {
	return UnavailableReason(ctx, info) == ""
}

func UnavailableReason(ctx context.Context, info share.Info) string {
	if info.ForegroundOnly && sandboxctx.BackgroundContext(ctx) {
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

func CanAccessTool(actor contextinfo.Actor, permission *security.Permission, info share.Info) bool {
	if info.SuperadminOnly && actor.Role != contextinfo.RoleSuperadmin {
		return false
	}
	risk := info.Risk
	if risk == "" {
		risk = share.RiskHigh
	}
	return permission.CanUseTool(actor, risk, info.OwnerScoped)
}

// QueryTools 对目录和按名称指定的依赖使用相同资格规则；权限策略由构造时注入。
func (r *Registry) QueryTools(ctx context.Context, query share.ToolQuery) ([]share.Info, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	actor, _ := contextinfo.ActorFromContext(ctx)
	r.mu.RLock()
	candidates := make([]share.Tool, 0, len(r.tools))
	for _, candidate := range r.tools {
		candidates = append(candidates, candidate.Tool)
	}
	r.mu.RUnlock()
	var infos []share.Info
	for _, candidate := range candidates {
		info := candidate.Info()
		if query.Names != nil && !slices.Contains(query.Names, info.Name) {
			continue
		}
		if (!query.IncludeHidden && info.Hidden) || (!query.IncludeDiscover && info.Name == "discover_tool") {
			continue
		}
		_, skill := candidate.(share.DetailProvider)
		if (query.Kind == "tool" && skill) || (query.Kind == "skill" && !skill) {
			continue
		}
		if !InfoAvailableInContext(ctx, info) || !CanAccessTool(actor, r.permission, info) {
			continue
		}
		info.APITypes = slices.Clone(info.APITypes)
		info.Tags = slices.Clone(info.Tags)
		info.DependsOn = slices.Clone(info.DependsOn)
		infos = append(infos, info)
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	return infos, nil
}

// BackgroundCachedTools applies background restrictions to executable discovery metadata.
func BackgroundCachedTools(ctx context.Context, items []share.DiscoveredTool) []share.DiscoveredTool {
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
		if allowed && InfoAvailableInContext(ctx, share.Info{ForegroundOnly: item.ForegroundOnly}) {
			result = append(result, item)
		}
	}
	return result
}
