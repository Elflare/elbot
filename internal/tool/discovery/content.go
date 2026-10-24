// 组织发现过程的动态说明、摘要、详情和错误正文。
package discovery

import toolshare "elbot/internal/tool/share"

import "strings"

// RenderDetailBlocks renders business details without session-dependent rules.
func RenderDetailBlocks(blocks []toolshare.DetailBlock) string {
	var parts []string
	for _, block := range blocks {
		if content := strings.TrimSpace(block.Content); content != "" {
			parts = append(parts, content)
		}
	}
	return strings.Join(parts, "\n\n---\n\n")
}
