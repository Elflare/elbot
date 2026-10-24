// 解析发现结果和激活声明，生成待提交的工具状态更新。
package discovery

import (
	"context"
	"encoding/json"
	"fmt"

	"elbot/internal/tool/share"
)

// DiscoveryStateUpdate 合并发现的 schema 与激活声明，交由调用方统一提交。
func (r *Resolver) DiscoveryStateUpdate(ctx context.Context, result *share.Result) (share.StateUpdate, error) {
	if err := ctx.Err(); err != nil {
		return share.StateUpdate{}, err
	}
	if result == nil {
		return share.StateUpdate{}, nil
	}
	update := share.StateUpdate{}
	if len(result.Data) > 0 {
		var discovery share.DiscoveryResult
		if err := json.Unmarshal(result.Data, &discovery); err != nil {
			return share.StateUpdate{}, fmt.Errorf("decode tool discovery: %w", err)
		}
		update.Tools = NativeCachedToolsFromDiscovery(&discovery)
	}
	activated, err := r.activatedTools(ctx, result.Metadata)
	if err != nil {
		return share.StateUpdate{}, err
	}
	update.Tools = append(update.Tools, activated...)
	return update, nil
}

func (r *Resolver) activatedTools(ctx context.Context, metadata map[string]any) ([]share.DiscoveredTool, error) {
	names := normalizeNames(metadataToolNames(metadata[share.MetadataActivateTools]))
	if len(names) == 0 {
		return nil, nil
	}
	tools, err := r.registry.QueryTools(ctx, share.ToolQuery{
		Names: names, IncludeHidden: true, IncludeDiscover: true,
	})
	if err != nil {
		return nil, err
	}
	allowed := make(map[string]share.Tool, len(tools))
	for _, tool := range tools {
		allowed[tool.Name()] = tool
	}
	discovery := &share.DiscoveryResult{}
	for _, name := range names {
		target, ok := allowed[name]
		if !ok {
			continue
		}
		schema := target.Schema()
		discovery.Tools = append(discovery.Tools, share.DiscoveryItem{Info: publicInfo(target.Info()), Schema: &schema})
	}
	return NativeCachedToolsFromDiscovery(discovery), nil
}

// NativeCachedToolsFromDiscovery 只转换可执行条目，并分离 schema 中的可变数据。
func NativeCachedToolsFromDiscovery(result *share.DiscoveryResult) []share.DiscoveredTool {
	if result == nil {
		return nil
	}
	out := make([]share.DiscoveredTool, 0, len(result.Tools))
	for _, discovered := range result.Tools {
		if discovered.Schema == nil || discovered.Info.Name == "" {
			continue
		}
		out = append(out, share.DiscoveredTool{
			Name: discovered.Info.Name, Source: share.SourceKindNative,
			Description: discovered.Info.Description, Schema: share.CloneSchema(*discovered.Schema),
			ForegroundOnly: discovered.Info.ForegroundOnly,
		})
	}
	return out
}

func metadataToolNames(value any) []string {
	switch names := value.(type) {
	case []string:
		return names
	case []any:
		out := make([]string, 0, len(names))
		for _, name := range names {
			if text, ok := name.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}
