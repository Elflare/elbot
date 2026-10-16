package tool

import "strings"

// RenderDetailBlocks renders business details without session-dependent rules.
func RenderDetailBlocks(blocks []DetailBlock) string {
	var parts []string
	for _, block := range blocks {
		if content := strings.TrimSpace(block.Content); content != "" {
			parts = append(parts, content)
		}
	}
	return strings.Join(parts, "\n\n---\n\n")
}
