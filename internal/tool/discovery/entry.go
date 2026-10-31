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
	resolver *Resolver // 解析已注册目录中的发现请求。
}

type discoverArgs struct {
	Name  string   `json:"name"`
	Names []string `json:"names"`
}

func NewDiscoverTool(resolver *Resolver) share.Tool {
	return discoverTool{resolver: resolver}
}

func (t discoverTool) Name() string { return "discover_tool" }

func (t discoverTool) Info() share.Info {
	return share.NewBuilder(t.Name()).
		Description("发现工具/Skill；name/names均空→简介列表，否则→schema或Skill内容。当前会话已有对应schema/内容则无需重复发现。").
		Risk(share.RiskSafe).
		BuildInfo()
}

func (t discoverTool) Schema() llm.ToolSchema {
	return share.NewBuilder(t.Name()).
		Description(t.Info().Description).
		String("name", "单个工具/Skill名称；可与names合并查询。").
		StringArray("names", "批量工具/Skill名称。").
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
		if err := ctx.Err(); err != nil {
			return nil, err.Error(), err
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
