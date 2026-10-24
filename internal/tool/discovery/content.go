// 组织发现过程的动态说明、摘要、详情和错误正文。
package discovery

import (
	"context"
	"fmt"
	"strings"

	"elbot/internal/tool/share"
)

// LoadDiscoveryContent 优先使用带 context 的动态说明提供者。
func LoadDiscoveryContent(ctx context.Context, target share.Tool) (content string, override bool, supported bool, err error) {
	if provider, ok := target.(share.ContextDiscoveryContentProvider); ok {
		content, override, err = provider.DiscoveryContent(ctx)
		return content, override, true, err
	}
	if provider, ok := target.(share.DiscoveryContentProvider); ok {
		content, override = provider.DiscoveryContent()
		return content, override, true, nil
	}
	return "", false, false, nil
}

func discoveryContent(ctx context.Context, targets map[string]share.Tool, result *share.DiscoveryResult) (share.DiscoveryText, error) {
	parts := []string{}
	foundTools := []string{}
	appendedExtras := []string{}
	detailBlocks := []share.DetailBlock{}
	for _, discovered := range result.Tools {
		name := strings.TrimSpace(discovered.Info.Name)
		if discovered.Detail != "" {
			foundTools = append(foundTools, name)
			block := discovered.DetailBlock
			if strings.TrimSpace(block.Content) == "" {
				block = share.DetailBlock{Content: discovered.Detail}
			}
			detailBlocks = append(detailBlocks, block)
			continue
		}
		if discovered.Schema != nil && name != "" {
			foundTools = append(foundTools, name)
			if target, ok := targets[name]; ok {
				content, override, supported, err := LoadDiscoveryContent(ctx, target)
				if err != nil {
					return share.DiscoveryText{}, fmt.Errorf("load discovery content for %s: %w", name, err)
				}
				if supported && strings.TrimSpace(content) != "" {
					if override {
						parts = append(parts, content)
					} else {
						appendedExtras = append(appendedExtras, content)
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
		failures := make([]string, 0, len(result.Errors))
		for _, item := range result.Errors {
			if item.Name == "" {
				continue
			}
			reason := strings.TrimSpace(item.Reason)
			if reason == "" {
				reason = "not found or not allowed"
			}
			failures = append(failures, fmt.Sprintf("%s：%s", item.Name, reason))
		}
		if len(failures) > 0 {
			parts = append(parts, "未发现工具："+strings.Join(failures, ", "))
		}
	}
	return share.DiscoveryText{Summary: summary, Details: strings.Join(parts, "\n\n")}, nil
}

// RenderDetailBlocks 只拼接业务详情，不处理会话相关的语言规则。
func RenderDetailBlocks(blocks []share.DetailBlock) string {
	var parts []string
	for _, block := range blocks {
		if content := strings.TrimSpace(block.Content); content != "" {
			parts = append(parts, content)
		}
	}
	return strings.Join(parts, "\n\n---\n\n")
}
