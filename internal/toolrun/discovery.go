package toolrun

import (
	"context"
	"encoding/json"
	"fmt"

	"elbot/internal/contextinfo"
	"elbot/internal/security"
	"elbot/internal/tool"
)

// DiscoveryStateUpdate collects a discovery's schemas, activated wrappers and
// shown rule cards so the caller can commit them together.
func DiscoveryStateUpdate(ctx context.Context, result *tool.Result, registry *tool.Registry, actor contextinfo.Actor, policy *security.Policy) (StateUpdate, error) {
	if result == nil {
		return StateUpdate{}, nil
	}
	update := StateUpdate{ShownRuleCardFormats: metadataToolNames(result.Metadata[tool.MetadataShownRuleCardFormats])}
	if len(result.Data) > 0 {
		var discovery tool.DiscoveryResult
		if err := json.Unmarshal(result.Data, &discovery); err != nil {
			return StateUpdate{}, fmt.Errorf("decode tool discovery: %w", err)
		}
		update.Tools = NativeCachedToolsFromDiscovery(&discovery)
	}
	update.Tools = append(update.Tools, activatedTools(ctx, result.Metadata, registry, actor, policy)...)
	return update, nil
}

func activatedTools(ctx context.Context, metadata map[string]any, registry *tool.Registry, actor contextinfo.Actor, policy *security.Policy) []CachedTool {
	if len(metadata) == 0 || registry == nil {
		return nil
	}
	names := metadataToolNames(metadata[tool.MetadataActivateTools])
	if len(names) == 0 {
		return nil
	}
	if policy == nil {
		policy = security.DefaultPolicy()
	}
	discovery := &tool.DiscoveryResult{}
	for _, name := range names {
		if t, ok := registry.Get(name); ok {
			info := t.Info()
			risk := info.Risk
			if risk == "" {
				risk = tool.RiskHigh
			}
			if !tool.InfoAvailableInContext(ctx, info) || !policy.CanUseTool(actor, risk, info.OwnerScoped) {
				continue
			}
			schema := t.Schema()
			discovery.Tools = append(discovery.Tools, tool.DiscoveredTool{Info: tool.PublicInfo{Name: name, Description: info.Description, Source: string(info.Source), ForegroundOnly: info.ForegroundOnly}, Schema: &schema})
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
