package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	"elbot/internal/security"
)

type discoverTool struct {
	registry       *Registry
	beforeDiscover func(context.Context) error
}

type discoverArgs struct {
	Name  string   `json:"name"`
	Names []string `json:"names"`
}

func NewDiscoverTool(registry *Registry, before ...func(context.Context) error) Tool {
	var hook func(context.Context) error
	if len(before) > 0 {
		hook = before[0]
	}
	return discoverTool{registry: registry, beforeDiscover: hook}
}

func (t discoverTool) Name() string {
	return "discover_tool"
}

func (t discoverTool) Info() Info {
	return NewBuilder(t.Name()).
		Description("发现可用工具和技能。未知工具schema时使用此工具获取，已知不再次使用。不传 name/names 时列出工具简介，传 name/names 时返回指定工具及 schema或者skill描述。").
		Risk(RiskSafe).
		BuildInfo()
}

func (t discoverTool) Schema() llm.ToolSchema {
	return NewBuilder(t.Name()).
		Description(t.Info().Description).
		String("name", "可选，要查询详情的tool/skill名称。不传 name/names 则列出全部可见工具简介。").
		StringArray("names", "可选，要批量查询详情的tool/skill名称列表。").
		BuildSchema()
}

func (t discoverTool) Call(ctx context.Context, req CallRequest) (_ *Result, logContent string, _ error) {
	defer func() { logContent = TruncateLogContent(logContent, 1024) }()
	if err := ctx.Err(); err != nil {
		return nil, err.Error(), err
	}
	if t.beforeDiscover != nil {
		if err := t.beforeDiscover(ctx); err != nil {
			return nil, err.Error(), err
		}
	}
	var args discoverArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			{
				callErr := fmt.Errorf("parse discover_tool arguments: %w", err)
				return nil, callErr.Error(), callErr
			}
		}
	}
	names := append([]string{}, args.Names...)
	if args.Name != "" {
		names = append([]string{args.Name}, names...)
	}
	names = normalizeNames(names)
	actor, permission := discoverySecurity(ctx)
	var result *DiscoveryResult
	activateTools := []string{}
	if len(names) == 0 {
		infos := t.registry.List()
		out := make([]DiscoveredTool, 0, len(infos))
		for _, info := range infos {
			if !info.Hidden && InfoAvailableInContext(ctx, info) && (CanAccessTool(actor, permission, info) || info.Name == "discover_tool") {
				out = append(out, DiscoveredTool{Info: publicInfo(info)})
			}
		}
		result = &DiscoveryResult{Tools: out}
	} else {
		details, errors := t.registry.DiscoverDetails(contextinfo.WithActor(ctx, actor), names, func(candidate Tool) bool {
			info := candidate.Info()
			return InfoAvailableInContext(ctx, info) && CanAccessTool(actor, permission, info)
		})
		for _, discovered := range details {
			if discovered.Detail == "" {
				continue
			}
			if target, ok := t.registry.Get(discovered.Info.Name); ok {
				if detailer, ok := target.(DetailProvider); ok {
					activateTools = append(activateTools, detailer.ActivateTools()...)
				}
			}
		}
		result = &DiscoveryResult{Tools: details, Errors: errors}
		if len(details) == 0 && len(errors) > 0 && len(names) == 1 {
			{
				callErr := fmt.Errorf("discover tool %q: %s", errors[0].Name, errors[0].Reason)
				return nil, callErr.Error(), callErr
			}
		}
	}
	data, err := marshalJSONNoEscape(result)
	if err != nil {
		{
			callErr := fmt.Errorf("marshal discovery result: %w", err)
			return nil, callErr.Error(), callErr
		}
	}
	metadata := map[string]any{}
	activateTools = normalizeNames(activateTools)
	if len(activateTools) > 0 {
		metadata[MetadataActivateTools] = activateTools
	}
	details, err := discoveryContent(ctx, t.registry, result)
	if err != nil {
		return nil, err.Error(), err
	}
	needsElyphRules := false
	for _, discovered := range result.Tools {
		needsElyphRules = needsElyphRules || discovered.NeedsElyphRules
	}
	// 普通工具的完整 schema 通过 Data 交给工具状态服务，协议路线负责发送。
	// tool message 独立保留发现说明，不重复展开完整 schema。
	logContent = fmt.Sprintf("found=%d errors=%d", len(result.Tools), len(result.Errors))
	for _, failure := range result.Errors {
		logContent += fmt.Sprintf("\n%s: %s", failure.Name, failure.Reason)
	}
	for _, discovered := range result.Tools {
		logContent += "\n" + discovered.Info.Name
	}
	{
		result := &Result{Content: strings.TrimSpace(details.Summary + "\n\n" + details.Details), DiscoveryText: &details, NeedsElyphRules: needsElyphRules, Data: data, Metadata: metadata}
		return result, logContent, nil
	}
}

func marshalJSONNoEscape(value any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSpace(buf.Bytes()), nil
}

func discoveryContent(ctx context.Context, registry *Registry, result *DiscoveryResult) (DiscoveryText, error) {
	if result == nil {
		return DiscoveryText{}, nil
	}
	parts := []string{}
	foundTools := []string{}
	appendedExtras := []string{}
	detailBlocks := []DetailBlock{}
	for _, discovered := range result.Tools {
		name := strings.TrimSpace(discovered.Info.Name)
		if discovered.Detail != "" {
			foundTools = append(foundTools, name)
			block := discovered.DetailBlock
			if strings.TrimSpace(block.Content) == "" {
				block = DetailBlock{Content: discovered.Detail}
			}
			detailBlocks = append(detailBlocks, block)
			continue
		}
		if discovered.Schema != nil && name != "" {
			foundTools = append(foundTools, name)
			if target, ok := registry.Get(name); ok {
				content, override, supported, err := LoadDiscoveryContent(ctx, target)
				if err != nil {
					return DiscoveryText{}, fmt.Errorf("load discovery content for %s: %w", name, err)
				}
				if supported {
					if strings.TrimSpace(content) != "" {
						if override {
							parts = append(parts, content)
						} else {
							appendedExtras = append(appendedExtras, content)
						}
						continue
					}
				}
			}
			continue
		}
		if name != "" {
			parts = append(parts, fmt.Sprintf("%s：%s", name, discovered.Info.Description))
		}
	}
	summary := ""
	if len(foundTools) > 0 {
		summary = fmt.Sprintf("已发现：%s。后续可直接使用。", strings.Join(foundTools, "、"))
	}
	parts = append(parts, appendedExtras...)
	if detailText := RenderDetailBlocks(detailBlocks); detailText != "" {
		parts = append(parts, detailText)
	}
	if len(result.Errors) > 0 {
		errors := make([]string, 0, len(result.Errors))
		for _, item := range result.Errors {
			if item.Name == "" {
				continue
			}
			reason := strings.TrimSpace(item.Reason)
			if reason == "" {
				reason = "not found or not allowed"
			}
			errors = append(errors, fmt.Sprintf("%s：%s", item.Name, reason))
		}
		if len(errors) > 0 {
			parts = append(parts, "未发现工具："+strings.Join(errors, ", "))
		}
	}
	return DiscoveryText{Summary: summary, Details: strings.Join(parts, "\n\n")}, nil
}

func discoverySecurity(ctx context.Context) (contextinfo.Actor, *security.Permission) {
	permission, ok := security.PermissionFromContext(ctx)
	if !ok || permission == nil {
		permission = security.DefaultPermission()
	}
	actor, ok := contextinfo.ActorFromContext(ctx)
	if !ok {
		actor = contextinfo.Actor{Role: contextinfo.RoleUser}
	}
	return actor, permission
}
