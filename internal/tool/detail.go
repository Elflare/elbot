package tool

import (
	"context"
	"sort"
	"strings"
)

type ruleCardStateContextKey struct{}

type ruleCardState struct {
	shown map[string]bool
	new   map[string]bool
}

func WithShownRuleCardFormats(ctx context.Context, formats []string) context.Context {
	state := &ruleCardState{shown: map[string]bool{}, new: map[string]bool{}}
	for _, format := range formats {
		format = strings.TrimSpace(format)
		if format != "" {
			state.shown[format] = true
		}
	}
	return context.WithValue(ctx, ruleCardStateContextKey{}, state)
}

func NewRuleCardFormatsFromContext(ctx context.Context) []string {
	state, ok := ctx.Value(ruleCardStateContextKey{}).(*ruleCardState)
	if !ok || state == nil || len(state.new) == 0 {
		return nil
	}
	out := make([]string, 0, len(state.new))
	for format := range state.new {
		if format != "" {
			out = append(out, format)
		}
	}
	sort.Strings(out)
	return out
}

func RenderDetailBlocks(blocks []DetailBlock) string {
	return renderDetailBlocks(context.Background(), blocks)
}

func RenderDetailBlocksWithContext(ctx context.Context, blocks []DetailBlock) string {
	return renderDetailBlocks(ctx, blocks)
}

func renderDetailBlocks(ctx context.Context, blocks []DetailBlock) string {
	parts := []string{}
	seenRules := map[string]bool{}
	for _, block := range blocks {
		content := strings.TrimSpace(block.Content)
		if content == "" {
			continue
		}
		rule := strings.TrimSpace(block.RuleCard)
		if rule != "" {
			key := ruleCardKey(block, rule)
			if !seenRules[key] {
				seenRules[key] = true
				if shouldRenderRuleCard(ctx, key) {
					parts = append(parts, rule)
				}
			}
		}
		parts = append(parts, content)
	}
	return strings.Join(parts, "\n\n---\n\n")
}

func ruleCardKey(block DetailBlock, rule string) string {
	key := strings.TrimSpace(block.Format)
	if key == "" {
		key = strings.TrimSpace(rule)
	}
	return key
}

func shouldRenderRuleCard(ctx context.Context, key string) bool {
	if ctx == nil || key == "" {
		return true
	}
	state, ok := ctx.Value(ruleCardStateContextKey{}).(*ruleCardState)
	if !ok || state == nil {
		return true
	}
	if state.shown[key] {
		return false
	}
	state.shown[key] = true
	state.new[key] = true
	return true
}
