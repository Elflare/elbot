// 管理工具注册、来源替换和目录查询。
package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"

	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	"elbot/internal/security"
	toolshare "elbot/internal/tool/share"
)

type Registry struct {
	mu         sync.RWMutex                        // 保护注册目录及原子替换。
	tools      map[string]toolshare.ToolDefinition // 名称到当前定义快照。
	permission interface {
		CanUseTool(contextinfo.Actor, security.RiskLevel, bool) bool
	} // 根部只交付目录筛选需要的权限能力。
}

func NewRegistry(permission interface {
	CanUseTool(contextinfo.Actor, security.RiskLevel, bool) bool
}) *Registry {
	return &Registry{tools: map[string]toolshare.ToolDefinition{}, permission: permission}
}

// RegisterTools 完整替换一组定义；远端只生成补丁，由会话事务发布。
func (r *Registry) RegisterTools(ctx context.Context, source toolshare.Source, scope toolshare.ToolRegistrationScope, definitions []toolshare.ToolDefinition) (toolshare.StateUpdate, error) {
	if err := ctx.Err(); err != nil {
		return toolshare.StateUpdate{}, err
	}
	switch source {
	case toolshare.SourceBuiltin, toolshare.SourceSkill:
		if scope.SessionID != "" || scope.EventKey != "" {
			return toolshare.StateUpdate{}, fmt.Errorf("global tool group cannot have session or event")
		}
	case toolshare.SourceELwisp:
		if scope.SessionID == "" || scope.EventKey == "" {
			return toolshare.StateUpdate{}, fmt.Errorf("elwisp registration requires session and event")
		}
	default:
		return toolshare.StateUpdate{}, fmt.Errorf("invalid tool source %d", source)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	next := make(map[string]toolshare.ToolDefinition, len(r.tools)+len(definitions))
	for name, definition := range r.tools {
		if source == toolshare.SourceELwisp || definition.Definition.Source != source {
			next[name] = definition
		}
	}
	update := toolshare.StateUpdate{}
	if source == toolshare.SourceELwisp {
		update.ReplaceGroups = []toolshare.ToolRegistrationScope{scope}
	}
	for _, definition := range definitions {
		if (source == toolshare.SourceELwisp) != (definition.Remote != nil) {
			return toolshare.StateUpdate{}, fmt.Errorf("tool %q execution does not match source %d", definition.Definition.Name, source)
		}
		definition, err := validateDefinition(definition)
		if err != nil {
			return toolshare.StateUpdate{}, err
		}
		definition.Definition.Source = source
		info := definition.Definition
		if _, exists := next[info.Name]; exists {
			return toolshare.StateUpdate{}, fmt.Errorf("tool %q already registered in this batch or another group", info.Name)
		}
		next[info.Name] = definition
		if source == toolshare.SourceELwisp {
			remote := definition.Remote
			if remote.EventKey != scope.EventKey {
				return toolshare.StateUpdate{}, fmt.Errorf("tool %q does not belong to remote event %q", info.Name, scope.EventKey)
			}
			update.Tools = append(update.Tools, toolshare.DiscoveredTool{Name: remote.Name, CanonicalName: info.Name, Source: toolshare.SourceKindELwisp, Risk: info.Risk, Description: info.Description, Schema: definition.Schema(), EventKey: remote.EventKey, Endpoint: remote.Endpoint, TimeoutSeconds: remote.TimeoutSeconds, ForegroundOnly: info.ForegroundOnly})
		}
	}
	if source != toolshare.SourceELwisp {
		r.tools = next
	}
	return update, nil
}

func validateDefinition(definition toolshare.ToolDefinition) (toolshare.ToolDefinition, error) {
	info := &definition.Definition
	info.Name = strings.TrimSpace(info.Name)
	info.Description = strings.TrimSpace(info.Description)
	if info.Name == "" {
		return definition, fmt.Errorf("tool name is required")
	}
	if definition.Callable() && info.Description == "" {
		return definition, fmt.Errorf("tool %q description is required", info.Name)
	}
	if definition.Callable() || info.Risk != "" {
		switch info.Risk {
		case toolshare.RiskSafe, toolshare.RiskLow, toolshare.RiskMedium, toolshare.RiskHigh, toolshare.RiskCritical:
		default:
			return definition, fmt.Errorf("tool %q requires a valid risk, got %q", info.Name, info.Risk)
		}
	}
	for _, apiType := range info.APITypes {
		if apiType != llm.APITypeChat && apiType != llm.APITypeResponse {
			return definition, fmt.Errorf("tool %q has invalid API type %q", info.Name, apiType)
		}
	}
	var err error
	if info.Tags, err = normalizeRegisteredNames(info.Tags, true); err != nil {
		return definition, fmt.Errorf("tool %q tags: %w", info.Name, err)
	}
	if info.DependsOn, err = normalizeRegisteredNames(info.DependsOn, false); err != nil {
		return definition, fmt.Errorf("tool %q dependencies: %w", info.Name, err)
	}
	if definition.ActivateTools, err = normalizeRegisteredNames(definition.ActivateTools, false); err != nil {
		return definition, fmt.Errorf("tool %q activation: %w", info.Name, err)
	}
	if definition.Prepare != nil && definition.Remote != nil {
		return definition, fmt.Errorf("tool %q has both local and remote execution", info.Name)
	}
	if definition.Callable() {
		if err := validateParameters(definition.Parameters); err != nil {
			return definition, fmt.Errorf("tool %q parameters: %w", info.Name, err)
		}
	} else if definition.Parameters != nil || definition.LoadDetail == nil {
		return definition, fmt.Errorf("entry %q requires execution or document detail", info.Name)
	}
	if definition.Remote != nil {
		remote := *definition.Remote
		remote.Endpoint = strings.TrimSpace(remote.Endpoint)
		endpoint, err := url.Parse(remote.Endpoint)
		if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
			return definition, fmt.Errorf("tool %q endpoint must be http or https URL", info.Name)
		}
		if strings.TrimSpace(remote.Name) == "" || strings.ContainsAny(remote.Name, ". /\\") || remote.CanonicalName != info.Name {
			return definition, fmt.Errorf("tool %q has invalid remote name", info.Name)
		}
		if remote.TimeoutSeconds < 0 || remote.TimeoutSeconds > 60 {
			return definition, fmt.Errorf("tool %q timeout_seconds must be between 0 and 60", info.Name)
		}
		if remote.TimeoutSeconds == 0 {
			remote.TimeoutSeconds = 10
		}
		definition.Remote = &remote
	}
	return definition.Snapshot(), nil
}

func normalizeRegisteredNames(names []string, tags bool) ([]string, error) {
	out := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if tags {
			name = toolshare.NormalizeToolTag(name)
		}
		if name == "" {
			return nil, fmt.Errorf("empty or invalid value")
		}
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out, nil
}

func validateParameters(parameters map[string]any) error {
	if parameters["type"] != "object" {
		return fmt.Errorf("type must be object")
	}
	if _, err := json.Marshal(parameters); err != nil {
		return fmt.Errorf("must be JSON data: %w", err)
	}
	properties, ok := parameters["properties"].(map[string]any)
	if !ok {
		return fmt.Errorf("properties must be an object (empty is allowed)")
	}
	var required []string
	switch value := parameters["required"].(type) {
	case nil:
	case []string:
		required = value
	case []any:
		for _, item := range value {
			name, ok := item.(string)
			if !ok {
				return fmt.Errorf("required must contain strings")
			}
			required = append(required, name)
		}
	default:
		return fmt.Errorf("required must be an array")
	}
	for _, name := range required {
		if _, ok := properties[name]; !ok {
			return fmt.Errorf("required parameter %q has no property", name)
		}
	}
	return nil
}

// Get 返回只读定义；查询结果中的 map、slice 不得原地修改。
func (r *Registry) Get(name string) (toolshare.ToolDefinition, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tool, ok := r.tools[strings.TrimSpace(name)]
	return tool, ok
}
