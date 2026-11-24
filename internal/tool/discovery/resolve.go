// 解析工具发现请求，展开依赖并加载 schema 与 Skill 详情。
package discovery

import (
	"context"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/tool/share"
)

// Resolver 使用已注册的目录解析发现请求；不触发 Skill 加载或提交会话状态。
type Resolver struct {
	registry interface {
		Query(context.Context, share.ToolQuery) ([]share.ToolDefinition, error)
	} // 提供工具目录与统一资格筛选。
}

func (r *Resolver) Availability(ctx context.Context) (contextinfo.ToolAvailability, error) {
	available, err := r.registry.Query(ctx, share.ToolQuery{IncludeHidden: true, IncludeDiscover: true})
	if err != nil {
		return contextinfo.ToolAvailability{}, err
	}
	definitions := make(map[string]share.ToolDefinition, len(available))
	for _, item := range available {
		definitions[item.Definition.Name] = item
	}
	const (
		visiting uint8 = iota + 1
		validState
		invalid
	)
	states := make(map[string]uint8, len(available))
	var isValid func(string) bool
	isValid = func(name string) bool {
		switch states[name] {
		case visiting, validState:
			return true
		case invalid:
			return false
		}
		definition, ok := definitions[name]
		if !ok {
			states[name] = invalid
			return false
		}
		states[name] = visiting
		for _, dependency := range definition.Definition.DependsOn {
			if !isValid(dependency) {
				states[name] = invalid
				return false
			}
		}
		states[name] = validState
		return true
	}
	snapshot := contextinfo.ToolAvailability{Allowed: make(map[string]struct{}, len(available))}
	for _, item := range available {
		name := item.Definition.Name
		if !isValid(name) {
			continue
		}
		snapshot.Allowed[name] = struct{}{}
		if item.Definition.Hidden || name == "discover_tool" {
			continue
		}
		if item.LoadDetail != nil {
			snapshot.Skills = append(snapshot.Skills, name)
		} else {
			snapshot.Tools = append(snapshot.Tools, name)
		}
	}
	return snapshot, nil
}

func NewResolver(registry interface {
	Query(context.Context, share.ToolQuery) ([]share.ToolDefinition, error)
}) *Resolver {
	return &Resolver{registry: registry}
}

// Discover 无名称时返回简介，有名称时返回详情及逐项错误。
func (r *Resolver) Discover(ctx context.Context, names []string) (*share.DiscoveryResult, error) {
	result, _, err := r.discover(ctx, names)
	return result, err
}

// discover 同时保留解析时取得的对象，供本次正文与激活声明继续使用。
func (r *Resolver) discover(ctx context.Context, names []string) (*share.DiscoveryResult, map[string]share.ToolDefinition, error) {
	names = normalizeNames(names)
	if len(names) > 0 {
		return r.resolve(ctx, names)
	}
	availability, _ := contextinfo.ToolAvailabilityFromContext(ctx)
	names = append(names, availability.Tools...)
	names = append(names, availability.Skills...)
	if _, ok := availability.Allowed["discover_tool"]; ok {
		names = append(names, "discover_tool")
	}
	tools, err := r.registry.Query(ctx, share.ToolQuery{Scope: share.QueryCatalog, Names: names, IncludeDiscover: true})
	if err != nil {
		return nil, nil, err
	}
	result := &share.DiscoveryResult{}
	for _, tool := range tools {
		result.Tools = append(result.Tools, share.DiscoveryItem{Info: publicInfo(tool.Definition)})
	}
	return result, nil, nil
}

// DiscoverDetails 允许批量部分成功；空名称集合返回空结果。
func (r *Resolver) DiscoverDetails(ctx context.Context, names []string) (*share.DiscoveryResult, error) {
	result, _, err := r.resolve(ctx, normalizeNames(names))
	return result, err
}

// Resolve 解析已经通过 Registry 筛选的根工具，不重新按名称查询根对象。
// 返回本次解析使用的根工具和依赖对象，供调用方继续读取动态内容。
func (r *Resolver) Resolve(ctx context.Context, tools []share.ToolDefinition) (*share.DiscoveryResult, map[string]share.ToolDefinition, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	result := &share.DiscoveryResult{}
	targets := map[string]share.ToolDefinition{}
	for _, target := range tools {
		targets[target.Definition.Name] = target
	}
	seen := map[string]bool{}
	for _, target := range tools {
		if err := r.addDiscoveryDetail(ctx, target, targets, seen, result); err != nil {
			return nil, nil, err
		}
	}
	return result, targets, nil
}

func (r *Resolver) resolve(ctx context.Context, names []string) (*share.DiscoveryResult, map[string]share.ToolDefinition, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	var roots []share.ToolDefinition
	var missing []share.DiscoveryError
	byName := map[string]share.ToolDefinition{}
	if len(names) > 0 {
		tools, err := r.registry.Query(ctx, share.ToolQuery{Scope: share.QueryAvailable, Names: names, IncludeDiscover: true})
		if err != nil {
			return nil, nil, err
		}
		for _, tool := range tools {
			byName[tool.Definition.Name] = tool
		}
	}
	for _, name := range names {
		tool, ok := byName[name]
		if !ok {
			missing = append(missing, share.DiscoveryError{Name: name, Reason: "not found or not allowed"})
			continue
		}
		roots = append(roots, tool)
	}
	result, targets, err := r.Resolve(ctx, roots)
	if err != nil {
		return nil, nil, err
	}
	result.Errors = append(missing, result.Errors...)
	return result, targets, nil
}

func (r *Resolver) addDiscoveryDetail(ctx context.Context, target share.ToolDefinition, targets map[string]share.ToolDefinition, seen map[string]bool, result *share.DiscoveryResult) error {
	name := target.Definition.Name
	if seen[name] {
		return nil
	}
	// 保留原对象，同时使循环关联只解析一次。
	targets[name] = target
	seen[name] = true
	info := target.Definition
	discovered := share.DiscoveryItem{Info: publicInfo(info), NeedsElyphRules: info.NeedsElyphRules || info.Source == share.SourceSkill}
	if loader := target.LoadDetail; loader != nil {
		block, err := loader(ctx)
		if err != nil {
			return err
		}
		discovered.DetailBlock = block
		discovered.Detail = share.RenderDetailBlocks([]share.DetailBlock{block})
	} else {
		schema := target.Schema()
		discovered.Schema = &schema
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	result.Tools = append(result.Tools, discovered)
	for _, dependency := range info.DependsOn {
		candidate, ok := targets[dependency]
		if !ok {
			tools, err := r.registry.Query(ctx, share.ToolQuery{Scope: share.QueryCatalog, Names: []string{dependency}, IncludeHidden: true, IncludeDiscover: true})
			if err != nil {
				return err
			}
			if len(tools) == 0 {
				continue
			}
			candidate = tools[0]
			targets[dependency] = candidate
		}
		if err := r.addDiscoveryDetail(ctx, candidate, targets, seen, result); err != nil {
			return err
		}
	}
	return nil
}

func normalizeNames(names []string) []string {
	out := make([]string, 0, len(names))
	seen := map[string]bool{}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

func publicInfo(info share.Info) share.PublicInfo {
	return share.PublicInfo{Name: info.Name, Description: info.Description, Source: info.Source, ForegroundOnly: info.ForegroundOnly}
}
