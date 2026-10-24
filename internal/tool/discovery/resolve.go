// 解析工具发现请求，展开依赖并加载 schema 与 Skill 详情。
package discovery

import (
	"context"
	"errors"
	"strings"

	"elbot/internal/elyph"
	"elbot/internal/tool/registry"
	"elbot/internal/tool/share"
)

// Resolver 使用已注册的目录解析发现请求；不触发 Skill 加载或提交会话状态。
type Resolver struct {
	registry *registry.Registry // 提供工具目录与统一资格筛选。
}

func NewResolver(registry *registry.Registry) *Resolver {
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

func (r *Resolver) resolve(ctx context.Context, names []string) (*share.DiscoveryResult, map[string]share.Tool, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	result := &share.DiscoveryResult{}
	targets := map[string]share.Tool{}
	for _, name := range names {
		if err := r.addDiscoveryDetail(ctx, name, true, targets, result); err != nil {
			return nil, nil, err
		}
	}
	return result, targets, nil
}

func (r *Resolver) addDiscoveryDetail(ctx context.Context, name string, root bool, targets map[string]share.Tool, result *share.DiscoveryResult) error {
	if _, seen := targets[name]; seen {
		return nil
	}
	tools, err := r.registry.QueryTools(ctx, share.ToolQuery{
		Names: []string{name}, IncludeHidden: !root, IncludeDiscover: true,
	})
	if err != nil {
		return err
	}
	if len(tools) == 0 {
		if root {
			result.Errors = append(result.Errors, share.DiscoveryError{Name: name, Reason: "not found or not allowed"})
		}
		return nil
	}
	target := tools[0]
	// 保留原对象，同时使循环关联只解析一次。
	targets[name] = target
	info := target.Info()
	discovered := share.DiscoveryItem{Info: publicInfo(info), NeedsElyphRules: info.NeedsElyphRules}
	if loader, ok := target.(share.LazyDetailProvider); ok {
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
		discovered.Detail = RenderDetailBlocks([]share.DetailBlock{block})
	} else if detailer, ok := target.(share.DetailProvider); ok {
		if structured, ok := target.(share.StructuredDetailProvider); ok {
			discovered.DetailBlock = structured.DetailBlock()
			discovered.Detail = RenderDetailBlocks([]share.DetailBlock{discovered.DetailBlock})
		} else {
			discovered.Detail = detailer.Detail()
			discovered.DetailBlock = share.DetailBlock{Content: discovered.Detail}
		}
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
		if err := r.addDiscoveryDetail(ctx, dependency, false, targets, result); err != nil {
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
