package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
		Description("发现可用工具。未知工具schema时使用此工具获取，已知不再次使用。不传 name/names 时列出工具简介，传 name/names 时返回指定工具及 schema。决定调用工具时，请先用一句简短自然语言告诉用户你在做什么。").
		Risk(RiskSafe).
		BuildInfo()
}

func (t discoverTool) Schema() llm.ToolSchema {
	return NewBuilder(t.Name()).
		Description(t.Info().Description).
		String("name", "可选，要查询详情的单个工具名称。不传 name/names 则列出全部可见工具简介。").
		StringArray("names", "可选，要批量查询详情的工具名称列表。查询主工具时会同时返回依赖工具详情。").
		BuildSchema()
}

func (t discoverTool) Call(ctx context.Context, req CallRequest) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if t.beforeDiscover != nil {
		if err := t.beforeDiscover(ctx); err != nil {
			return nil, err
		}
	}
	var args discoverArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			return nil, fmt.Errorf("parse discover_tool arguments: %w", err)
		}
	}
	names := append([]string{}, args.Names...)
	if args.Name != "" {
		names = append([]string{args.Name}, names...)
	}
	names = normalizeNames(names)
	actor, policy := discoverySecurity(ctx)
	var result *DiscoveryResult
	activateTools := []string{}
	if len(names) == 0 {
		infos := t.registry.List()
		out := make([]DiscoveredTool, 0, len(infos))
		for _, info := range infos {
			if !info.Hidden && InfoAvailableInContext(ctx, info) && (CanAccessTool(actor, policy, info) || info.Name == "discover_tool") {
				out = append(out, DiscoveredTool{Info: publicInfo(info)})
			}
		}
		result = &DiscoveryResult{Tools: out}
	} else {
		details, errors := t.registry.DiscoverDetails(names, func(candidate Tool) bool {
			info := candidate.Info()
			return InfoAvailableInContext(ctx, info) && CanAccessTool(actor, policy, info)
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
			return nil, fmt.Errorf("tool %q not found or not allowed", names[0])
		}
	}
	data, err := marshalJSONNoEscape(result)
	if err != nil {
		return nil, fmt.Errorf("marshal discovery result: %w", err)
	}
	metadata := map[string]any{}
	activateTools = normalizeNames(activateTools)
	if len(activateTools) > 0 {
		metadata[MetadataActivateTools] = activateTools
	}
	content := discoveryContent(result)
	// 普通工具的完整 schema 通过 Data 交给 Agent 注入 top-level tools。
	// tool message 文本只返回简短“已发现工具”，避免上下文膨胀，
	// 也让后续请求保持稳定的工具注入顺序。
	return &Result{Content: content, Data: data, Metadata: metadata}, nil
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

func discoveryContent(result *DiscoveryResult) string {
	if result == nil {
		return ""
	}
	parts := []string{}
	foundTools := []string{}
	detailBlocks := []DetailBlock{}
	for _, discovered := range result.Tools {
		name := strings.TrimSpace(discovered.Info.Name)
		if discovered.Detail != "" {
			block := discovered.DetailBlock
			if strings.TrimSpace(block.Content) == "" {
				block = DetailBlock{Content: discovered.Detail}
			}
			detailBlocks = append(detailBlocks, block)
			continue
		}
		if discovered.Schema != nil && name != "" {
			foundTools = append(foundTools, name)
			continue
		}
		if name != "" {
			parts = append(parts, fmt.Sprintf("%s：%s", name, discovered.Info.Description))
		}
	}
	if len(foundTools) > 0 {
		parts = append([]string{fmt.Sprintf("已发现工具：%s。后续可直接调用。", strings.Join(foundTools, ", "))}, parts...)
	}
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
	return strings.Join(parts, "\n\n")
}

func discoverySecurity(ctx context.Context) (security.Actor, *security.Policy) {
	policy, ok := security.PolicyFromContext(ctx)
	if !ok || policy == nil {
		policy = security.DefaultPolicy()
	}
	actor, ok := security.ActorFromContext(ctx)
	if !ok {
		actor = security.Actor{Role: security.RoleUser}
	}
	return actor, policy
}
