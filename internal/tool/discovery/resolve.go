// 解析工具发现请求，展开依赖并加载 schema 与 Skill 详情。
package discovery

import (
	"context"
	"errors"
	"strings"

	"elbot/internal/elyph"
	"elbot/internal/tool/share"
)

// Resolver 使用已注册的目录解析发现请求；不触发 Skill 加载或提交会话状态。
type Resolver struct {
	registry interface {
		QueryTools(context.Context, share.ToolQuery) ([]share.Tool, error)
	} // 提供工具目录与统一资格筛选。
}

func NewResolver(registry interface {
	QueryTools(context.Context, share.ToolQuery) ([]share.Tool, error)
}) *Resolver {
	return &Resolver{registry: registry}
}

// Discover 无名称时返回简介，有名称时返回详情及逐项错误。
func (r *Resolver) Discover(ctx context.Context, names []string) (*share.DiscoveryResult, error) {
	result, _, err := r.discover(ctx, names)
	return result, err
}

// discover 同时保留解析时取得的对象，供本次正文与激活声明继续使用。
func (r *Resolver) discover(ctx context.Context, names []string) (*share.DiscoveryResult, map[string]share.Tool, error) {
	names = normalizeNames(names)
	if len(names) > 0 {
		return r.resolve(ctx, names)
	}
	tools, err := r.registry.QueryTools(ctx, share.ToolQuery{IncludeDiscover: true})
	if err != nil {
		return nil, nil, err
	}
	result := &share.DiscoveryResult{}
	for _, tool := range tools {
		result.Tools = append(result.Tools, share.DiscoveryItem{Info: publicInfo(tool.Info())})
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
func (r *Resolver) Resolve(ctx context.Context, tools []share.Tool) (*share.DiscoveryResult, map[string]share.Tool, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	result := &share.DiscoveryResult{}
	targets := map[string]share.Tool{}
	seen := map[string]bool{}
	for _, target := range tools {
		targets[target.Name()] = target
	}
	for _, target := range tools {
		if err := r.addDiscoveryDetail(ctx, target, targets, seen, result); err != nil {
			return nil, nil, err
		}
	}
	return result, targets, nil
}

func (r *Resolver) resolve(ctx context.Context, names []string) (*share.DiscoveryResult, map[string]share.Tool, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	result := &share.DiscoveryResult{}
	targets := map[string]share.Tool{}
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			continue
		}
		tools, err := r.registry.QueryTools(ctx, share.ToolQuery{Names: []string{name}, IncludeDiscover: true})
		if err != nil {
			return nil, nil, err
		}
		if len(tools) == 0 {
			result.Errors = append(result.Errors, share.DiscoveryError{Name: name, Reason: "not found or not allowed"})
			continue
		}
		if err := r.addDiscoveryDetail(ctx, tools[0], targets, seen, result); err != nil {
			return nil, nil, err
		}
	}
	return result, targets, nil
}

func (r *Resolver) addDiscoveryDetail(ctx context.Context, target share.Tool, targets map[string]share.Tool, seen map[string]bool, result *share.DiscoveryResult) error {
	name := target.Name()
	if seen[name] {
		return nil
	}
	// 保留原对象，同时使循环关联只解析一次。
	targets[name] = target
	seen[name] = true
	info := target.Info()
	discovered := share.DiscoveryItem{Info: publicInfo(info), NeedsElyphRules: info.NeedsElyphRules}
	if loader, ok := target.(share.DetailProvider); ok {
		block, err := loader.LoadDetail(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			result.Errors = append(result.Errors, share.DiscoveryError{Name: name, Reason: err.Error()})
			return nil
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
	discovered.NeedsElyphRules = discovered.NeedsElyphRules || (discovered.Detail != "" && discovered.DetailBlock.Format == elyph.Format)
	result.Tools = append(result.Tools, discovered)
	for _, dependency := range normalizeNames(info.DependsOn) {
		candidate, ok := targets[dependency]
		if !ok {
			tools, err := r.registry.QueryTools(ctx, share.ToolQuery{
				Names: []string{dependency}, IncludeHidden: true, IncludeDiscover: true,
			})
			if err != nil {
				return err
			}
			if len(tools) == 0 {
				continue
			}
			candidate = tools[0]
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
	return share.PublicInfo{Name: info.Name, Description: info.Description, Source: string(info.Source), ForegroundOnly: info.ForegroundOnly}
}
