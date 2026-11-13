// 实现 discover_tool 入口，处理工具定义、参数和调用结果。
package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"elbot/internal/operationresult"
	"elbot/internal/tool/share"
)

type discoverTool struct {
	resolver *Resolver // 解析已注册目录中的发现请求。
}

type discoverArgs struct {
	Name  string   `json:"name"`
	Names []string `json:"names"`
}

func NewDiscoverTool(resolver *Resolver) share.ToolDefinition {
	t := discoverTool{resolver: resolver}
	builder := share.NewBuilder("discover_tool").
		Description("发现工具/Skill；name/names均空→简介列表，否则→schema或Skill内容。当前会话已有对应schema/内容则无需重复发现。").
		Risk(share.RiskSafe)

	return builder.
		String("name", "单个工具/Skill名称；可与names合并查询。").
		StringArray("names", "批量工具/Skill名称。").Build(t.PrepareCall)
}

func (t discoverTool) PrepareCall(ctx context.Context, req share.CallRequest, info share.Info) (operationresult.Result[*share.PreparedCall], error) {
	var args discoverArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			return operationresult.Result[*share.PreparedCall]{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("parse discover_tool arguments: %v", err)}, nil
		}
	}
	names := append([]string{}, args.Names...)
	if args.Name != "" {
		names = append([]string{args.Name}, names...)
	}
	return operationresult.Result[*share.PreparedCall]{Status: operationresult.Success, Data: share.NewPreparedCall(req, share.RiskAssessment{Level: info.Risk}, []share.ConfirmationDetailBlock{share.TextConfirmationDetail(info.Description)}, func(ctx context.Context) (outcome operationresult.Result[*share.Result], logContent string, _ error) {
		if err := ctx.Err(); err != nil {
			return operationresult.Result[*share.Result]{}, err.Error(), err
		}
		result, targets, err := t.resolver.discover(ctx, names)
		if err != nil {
			return operationresult.Result[*share.Result]{}, err.Error(), err
		}
		// 全部目标被拒绝时返回拒绝；部分成功保留逐项发现结果。
		if len(result.Tools) == 0 && len(result.Errors) > 0 {
			messages := make([]string, 0, len(result.Errors))
			for _, failure := range result.Errors {
				messages = append(messages, fmt.Sprintf("discover tool %q: %s", failure.Name, failure.Reason))
			}
			message := strings.Join(messages, "\n")
			return operationresult.Result[*share.Result]{Status: operationresult.Rejected, Message: message}, message, nil
		}
		activateTools := []string{}
		for _, discovered := range result.Tools {
			if discovered.Detail == "" {
				continue
			}
			activateTools = append(activateTools, targets[discovered.Info.Name].ActivateTools...)
		}
		data, err := marshalJSONNoEscape(result)
		if err != nil {
			callErr := fmt.Errorf("marshal discovery result: %w", err)
			return operationresult.Result[*share.Result]{}, callErr.Error(), callErr
		}
		metadata := map[string]any{}
		activateTools = normalizeNames(activateTools)
		if len(activateTools) > 0 {
			metadata[share.MetadataActivateTools] = activateTools
		}
		details, err := discoveryContent(ctx, targets, result)
		if err != nil {
			return operationresult.Result[*share.Result]{}, err.Error(), err
		}
		needsElyphRules := false
		for _, discovered := range result.Tools {
			needsElyphRules = needsElyphRules || discovered.NeedsElyphRules
		}
		// schema 通过 Data 交给工具状态流程，正文只保留发现说明。
		logContent = fmt.Sprintf("found=%d errors=%d", len(result.Tools), len(result.Errors))
		return operationresult.Result[*share.Result]{Status: operationresult.Success, Data: &share.Result{
			Content:       strings.TrimSpace(details.Summary + "\n\n" + details.Details),
			DiscoveryText: &details, NeedsElyphRules: needsElyphRules, Data: data, Metadata: metadata,
		}}, logContent, nil
	})}, nil
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
