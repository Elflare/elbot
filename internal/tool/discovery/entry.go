// 实现 discover_tool 入口，处理工具定义、参数和调用结果。
package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"elbot/internal/llm"
	"elbot/internal/tool/share"
)

type discoverTool struct {
	resolver       *Resolver                   // 解析已注册目录中的发现请求。
	beforeDiscover func(context.Context) error // 仅工具入口触发的 Skill 加载回调。
}

type discoverArgs struct {
	Name  string   `json:"name"`
	Names []string `json:"names"`
}

func NewDiscoverTool(resolver *Resolver, before ...func(context.Context) error) share.Tool {
	var hook func(context.Context) error
	if len(before) > 0 {
		hook = before[0]
	}
	return discoverTool{resolver: resolver, beforeDiscover: hook}
}

func (t discoverTool) Name() string { return "discover_tool" }

func (t discoverTool) Info() share.Info {
	return share.NewBuilder(t.Name()).
		Description("发现可用工具和技能。未知工具schema时使用此工具获取，已知不再次使用。不传 name/names 时列出工具简介，传 name/names 时返回指定工具及 schema或者skill描述。").
		Risk(share.RiskSafe).
		BuildInfo()
}

func (t discoverTool) Schema() llm.ToolSchema {
	return share.NewBuilder(t.Name()).
		Description(t.Info().Description).
		String("name", "可选，要查询详情的tool/skill名称。不传 name/names 则列出全部可见工具简介。").
		StringArray("names", "可选，要批量查询详情的tool/skill名称列表。").
		BuildSchema()
}

func (t discoverTool) PrepareCall(ctx context.Context, req share.CallRequest) (*share.PreparedCall, error) {
	var args discoverArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			callErr := fmt.Errorf("parse discover_tool arguments: %w", err)
			return nil, callErr
		}
	}
	names := append([]string{}, args.Names...)
	if args.Name != "" {
		names = append([]string{args.Name}, names...)
	}
	return share.NewPreparedCall(req, share.RiskAssessment{Level: t.Info().Risk}, t.Info().Description, func(ctx context.Context) (_ *share.Result, logContent string, _ error) {
		defer func() { logContent = share.TruncateLogContent(logContent, 1024) }()
		if err := ctx.Err(); err != nil {
			return nil, err.Error(), err
		}
		if t.beforeDiscover != nil {
			if err := t.beforeDiscover(ctx); err != nil {
				return nil, err.Error(), err
			}
		}
		result, targets, err := t.resolver.discover(ctx, names)
		if err != nil {
			return nil, err.Error(), err
		}
		// 单个目标失败作为工具调用错误返回；批量发现保留逐项结果。
		if len(result.Tools) == 0 && len(result.Errors) == 1 {
			callErr := fmt.Errorf("discover tool %q: %s", result.Errors[0].Name, result.Errors[0].Reason)
			return nil, callErr.Error(), callErr
		}
		activateTools := []string{}
		for _, discovered := range result.Tools {
			if discovered.Detail == "" {
				continue
			}
			if detailer, ok := targets[discovered.Info.Name].(share.DetailProvider); ok {
				activateTools = append(activateTools, detailer.ActivateTools()...)
			}
		}
		data, err := marshalJSONNoEscape(result)
		if err != nil {
			callErr := fmt.Errorf("marshal discovery result: %w", err)
			return nil, callErr.Error(), callErr
		}
		metadata := map[string]any{}
		activateTools = normalizeNames(activateTools)
		if len(activateTools) > 0 {
			metadata[share.MetadataActivateTools] = activateTools
		}
		details, err := discoveryContent(ctx, targets, result)
		if err != nil {
			return nil, err.Error(), err
		}
		needsElyphRules := false
		for _, discovered := range result.Tools {
			needsElyphRules = needsElyphRules || discovered.NeedsElyphRules
		}
		// schema 通过 Data 交给工具状态流程，正文只保留发现说明。
		logContent = fmt.Sprintf("found=%d errors=%d", len(result.Tools), len(result.Errors))
		for _, failure := range result.Errors {
			logContent += fmt.Sprintf("\n%s: %s", failure.Name, failure.Reason)
		}
		for _, discovered := range result.Tools {
			logContent += "\n" + discovered.Info.Name
		}
		return &share.Result{
			Content:       strings.TrimSpace(details.Summary + "\n\n" + details.Details),
			DiscoveryText: &details, NeedsElyphRules: needsElyphRules, Data: data, Metadata: metadata,
		}, logContent, nil
	}), nil
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
