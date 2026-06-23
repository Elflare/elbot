package tool

import "strings"

func RenderDetailBlocks(blocks []DetailBlock) string {
	parts := []string{}
	seenRules := map[string]bool{}
	for _, block := range blocks {
		content := strings.TrimSpace(block.Content)
		if content == "" {
			continue
		}
		rule := strings.TrimSpace(block.RuleCard)
		if rule != "" {
			key := strings.TrimSpace(block.Format)
			if key == "" {
				key = rule
			}
			if !seenRules[key] {
				seenRules[key] = true
				parts = append(parts, rule)
			}
		}
		parts = append(parts, content)
	}
	return strings.Join(parts, "\n\n---\n\n")
}
