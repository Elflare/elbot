// 解析发现结果和激活声明，生成待提交的工具状态更新。
package discovery

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"encoding/json"
	"fmt"

	"elbot/internal/contextinfo"
	"elbot/internal/security"
	"elbot/internal/tool"
)

// DiscoveryStateUpdate collects a discovery's schemas and activated wrappers
// so the caller can commit them together.
func DiscoveryStateUpdate(ctx context.Context, result *toolshare.Result, registry *tool.Registry, actor contextinfo.Actor, permission *security.Permission) (toolshare.StateUpdate, error) {
	if result == nil {
		return toolshare.StateUpdate{}, nil
	}
	update := toolshare.StateUpdate{}
	if len(result.Data) > 0 {
		var discovery toolshare.DiscoveryResult
		if err := json.Unmarshal(result.Data, &discovery); err != nil {
			return toolshare.StateUpdate{}, fmt.Errorf("decode tool discovery: %w", err)
		}
		update.Tools = NativeCachedToolsFromDiscovery(&discovery)
	}
	update.Tools = append(update.Tools, activatedTools(ctx, result.Metadata, registry, actor, permission)...)
	return update, nil
}

func activatedTools(ctx context.Context, metadata map[string]any, registry *tool.Registry, actor contextinfo.Actor, permission *security.Permission) []toolshare.DiscoveredTool {
	if len(metadata) == 0 || registry == nil {
		return nil
	}
	names := metadataToolNames(metadata[toolshare.MetadataActivateTools])
	if len(names) == 0 {
		return nil
	}
	if permission == nil {
		permission = security.DefaultPermission()
	}
	discovery := &toolshare.DiscoveryResult{}
	for _, name := range names {
		if t, ok := registry.Get(name); ok {
			info := t.Info()
			risk := info.Risk
			if risk == "" {
				risk = toolshare.RiskHigh
			}
			if !tool.InfoAvailableInContext(ctx, info) || !permission.CanUseTool(actor, risk, info.OwnerScoped) {
				continue
			}
			schema := t.Schema()
			discovery.Tools = append(discovery.Tools, toolshare.DiscoveryItem{Info: toolshare.PublicInfo{Name: name, Description: info.Description, Source: string(info.Source), ForegroundOnly: info.ForegroundOnly}, Schema: &schema})
		}
	}
	return NativeCachedToolsFromDiscovery(discovery)
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
