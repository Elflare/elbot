package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"elbot/internal/directive"
	"elbot/internal/security"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/toolrun"
	"elbot/internal/workspace"
)

type toolDirectiveResult struct {
	update   toolrun.StateUpdate
	Text     string
	Injected []string
	Existing []string
	Invalid  []string
	Err      error
}

type skillDirectiveResult struct {
	update           toolrun.StateUpdate
	Err              error
	Text             string
	Skills           []string
	InjectedWrappers []string
	ExistingWrappers []string
	Invalid          []string
}

func (a *Agent) prepareToolDirectives(ctx context.Context, session *storage.Session, text string) toolDirectiveResult {
	result := toolDirectiveResult{Text: text}
	if session == nil || session.Mode != storage.SessionModeWork || a.toolRuntime.registry == nil || !containsAny(text, directive.ToolPrefix, directive.ToolFullPrefix, directive.ToolShortPrefix, directive.ToolShortFull) {
		return result
	}
	if !isBackgroundSession(session) {
		ctx = workspace.WithWorkspaceStore(ctx, a.workspaceStore(session))
	}
	matches := directive.ToolMatches(text)
	if len(matches) == 0 {
		return result
	}

	remove := make([]bool, len(matches))
	update := toolrun.StateUpdate{}
	seenDiscoveryContent := map[string]bool{}
	discoveryContent := []string{}
	for i, match := range matches {
		name := match.Name
		discovery, tagName, ok := a.discoveryForToolDirective(ctx, name)
		if !ok || discovery == nil || len(discovery.Tools) == 0 {
			result.Invalid = append(result.Invalid, name)
			continue
		}
		content, err := a.preloadedContextDiscoveryContent(ctx, discovery, seenDiscoveryContent)
		if err != nil {
			result.Err = err
			return result
		}
		if strings.TrimSpace(content) != "" {
			discoveryContent = append(discoveryContent, content)
		}
		update.Tools = append(update.Tools, toolrun.NativeCachedToolsFromDiscovery(discovery)...)
		if tagName != "" {
			update.Tags = append(update.Tags, tagName)
		}
		remove[i] = true
	}
	if len(update.Tools) == 0 {
		return result
	}
	result.update = update
	result.Text = directive.StripToolMatches(text, matches, remove)
	if len(discoveryContent) > 0 {
		if strings.TrimSpace(result.Text) == "" {
			result.Text = strings.Join(discoveryContent, "\n\n")
		} else {
			result.Text = strings.TrimSpace(result.Text) + "\n\n" + strings.Join(discoveryContent, "\n\n")
		}
	}
	return result
}

func (a *Agent) preloadedContextDiscoveryContent(ctx context.Context, discovery *tool.DiscoveryResult, seen map[string]bool) (string, error) {
	if discovery == nil || a.toolRuntime.registry == nil {
		return "", nil
	}
	parts := []string{}
	for _, discovered := range discovery.Tools {
		name := strings.TrimSpace(discovered.Info.Name)
		if discovered.Schema == nil || name == "" || seen[name] {
			continue
		}
		target, ok := a.toolRuntime.registry.Get(name)
		if !ok {
			continue
		}
		if _, ok := target.(tool.ContextDiscoveryContentProvider); !ok {
			continue
		}
		seen[name] = true
		content, _, _, err := tool.LoadDiscoveryContent(ctx, target)
		if err != nil {
			return "", fmt.Errorf("load discovery content for %s: %w", name, err)
		}
		if strings.TrimSpace(content) != "" {
			parts = append(parts, content)
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

func (a *Agent) prepareSkillDirectives(ctx context.Context, session *storage.Session, text string) skillDirectiveResult {
	result := skillDirectiveResult{Text: text}
	if session == nil || session.Mode != storage.SessionModeWork || a.toolRuntime.registry == nil || !containsAny(text, directive.SkillPrefix, directive.SkillFullPrefix, directive.SkillShortPrefix, directive.SkillShortFull) {
		return result
	}
	matches := directive.SkillMatches(text)
	if len(matches) == 0 {
		return result
	}
	state, err := a.toolState.Snapshot(ctx, session.ID)
	if err != nil {
		result.Err = err
		return result
	}
	ctx = tool.WithShownRuleCardFormats(ctx, state.ShownRuleCardFormats)
	policy := a.securityPolicy
	if policy == nil {
		policy = security.DefaultPolicy()
	}
	actor := a.actor(ctx)
	remove := make([]bool, len(matches))
	seenSkills := map[string]bool{}
	update := toolrun.StateUpdate{}
	blocks := []tool.DetailBlock{}
	for i, match := range matches {
		name := strings.TrimSpace(match.Name)
		candidate, ok := a.toolRuntime.registry.Get(name)
		if !ok || !a.canPreloadSkill(actor, policy, candidate) {
			result.Invalid = append(result.Invalid, name)
			continue
		}
		detailer := candidate.(tool.DetailProvider)
		if !seenSkills[name] {
			block, err := skillDetailBlock(security.WithActor(ctx, actor), candidate, detailer)
			if err != nil {
				result.Invalid = append(result.Invalid, name)
				a.audit("skill_preload_failed", "session_id", session.ID, "tool", name, "error", err)
				continue
			}
			seenSkills[name] = true
			result.Skills = append(result.Skills, name)
			blocks = append(blocks, block)
		}
		for _, wrapper := range detailer.ActivateTools() {
			update.Tools = append(update.Tools, a.preloadSkillWrapper(ctx, session, wrapper, actor, policy)...)
		}
		remove[i] = true
	}
	if len(result.Skills) == 0 {
		return result
	}
	stripped := directive.StripToolMatches(text, matches, remove)
	if detailText := tool.RenderDetailBlocksWithContext(ctx, blocks); detailText != "" {
		if strings.TrimSpace(stripped) == "" {
			stripped = detailText
		} else {
			stripped = strings.TrimSpace(stripped) + "\n\n" + detailText
		}
	}
	update.ShownRuleCardFormats = tool.NewRuleCardFormatsFromContext(ctx)
	result.update = update
	result.Text = stripped
	return result
}

func (a *Agent) discoveryForToolDirective(ctx context.Context, value string) (*tool.DiscoveryResult, string, bool) {
	value = strings.TrimSpace(value)
	if value == "" || a.toolRuntime.registry == nil {
		return nil, "", false
	}
	policy := a.securityPolicy
	if policy == nil {
		policy = security.DefaultPolicy()
	}
	actor := a.actor(ctx)
	if root, ok := a.toolRuntime.registry.Get(value); ok {
		if !a.canPreloadToolRoot(actor, policy, root) {
			return nil, "", false
		}
		discovery, ok := a.discoveryForToolNames(ctx, []string{value}, actor, policy)
		return discovery, "", ok
	}
	tagName := normalizeToolTag(value)
	names := a.namesByToolTag(ctx, tagName, func(candidate tool.Tool) bool {
		return a.canPreloadToolRoot(actor, policy, candidate)
	})
	if len(names) == 0 {
		return nil, "", false
	}
	discovery, ok := a.discoveryForToolNames(ctx, names, actor, policy)
	return discovery, tagName, ok
}

func (a *Agent) canPreloadToolRoot(actor security.Actor, policy *security.Policy, candidate tool.Tool) bool {
	info := candidate.Info()
	if info.Name == "discover_tool" || info.Hidden || !tool.CanAccessTool(actor, policy, info) {
		return false
	}
	_, isSkillLike := candidate.(tool.DetailProvider)
	return !isSkillLike
}

func (a *Agent) canPreloadSkill(actor security.Actor, policy *security.Policy, candidate tool.Tool) bool {
	info := candidate.Info()
	if info.Hidden || !tool.CanAccessTool(actor, policy, info) {
		return false
	}
	_, isSkillLike := candidate.(tool.DetailProvider)
	return isSkillLike
}

func containsAny(text string, values ...string) bool {
	for _, value := range values {
		if strings.Contains(text, value) {
			return true
		}
	}
	return false
}

func skillDetailBlock(ctx context.Context, candidate tool.Tool, detailer tool.DetailProvider) (tool.DetailBlock, error) {
	if loader, ok := candidate.(tool.LazyDetailProvider); ok {
		return loader.LoadDetail(ctx)
	}
	if structured, ok := candidate.(tool.StructuredDetailProvider); ok {
		return structured.DetailBlock(), nil
	}
	return tool.DetailBlock{Content: detailer.Detail()}, nil
}

func (a *Agent) preloadSkillWrapper(ctx context.Context, session *storage.Session, name string, actor security.Actor, policy *security.Policy) []toolrun.CachedTool {
	name = strings.TrimSpace(name)
	if name == "" || name == "discover_tool" || a.toolRuntime.registry == nil {
		return nil
	}
	candidate, ok := a.toolRuntime.registry.Get(name)
	if !ok || !tool.InfoAvailableInContext(ctx, candidate.Info()) || !tool.CanAccessTool(actor, policy, candidate.Info()) {
		a.audit("skill_wrapper_preload_skipped", "session_id", session.ID, "tool", name, "reason", "not_found_or_not_allowed")
		return nil
	}
	if _, isSkillLike := candidate.(tool.DetailProvider); isSkillLike {
		a.audit("skill_wrapper_preload_skipped", "session_id", session.ID, "tool", name, "reason", "skill_has_no_schema")
		return nil
	}
	schema := candidate.Schema()
	if schema.Function.Name == "" {
		a.audit("skill_wrapper_preload_skipped", "session_id", session.ID, "tool", name, "reason", "empty_schema")
		return nil
	}
	info := candidate.Info()
	discovery := &tool.DiscoveryResult{Tools: []tool.DiscoveredTool{{Info: tool.PublicInfo{Name: info.Name, Description: info.Description, Source: string(info.Source), ForegroundOnly: info.ForegroundOnly}, Schema: &schema}}}
	return toolrun.NativeCachedToolsFromDiscovery(discovery)
}

func (a *Agent) discoveryForToolNames(ctx context.Context, names []string, actor security.Actor, policy *security.Policy) (*tool.DiscoveryResult, bool) {
	details, _ := a.toolRuntime.registry.DiscoverDetails(security.WithActor(ctx, actor), names, func(candidate tool.Tool) bool {
		info := candidate.Info()
		return tool.InfoAvailableInContext(ctx, info) && tool.CanAccessTool(actor, policy, info)
	})
	if len(details) == 0 {
		return nil, false
	}
	out := &tool.DiscoveryResult{}
	for _, discovered := range details {
		if discovered.Schema == nil || discovered.Info.Name == "" || discovered.Detail != "" {
			continue
		}
		out.Tools = append(out.Tools, discovered)
	}
	return out, len(out.Tools) > 0
}

func (a *Agent) notifyToolDirectiveResult(ctx context.Context, result toolDirectiveResult) {
	parts := []string{}
	if len(result.Injected) > 0 {
		parts = append(parts, "已注入工具："+strings.Join(sortedUnique(result.Injected), ", "))
	}
	if len(result.Existing) > 0 {
		parts = append(parts, "已存在工具："+strings.Join(sortedUnique(result.Existing), ", "))
	}
	if len(result.Invalid) > 0 {
		parts = append(parts, "未找到或不可用的工具："+strings.Join(sortedUnique(result.Invalid), ", "))
	}
	if len(parts) == 0 {
		return
	}
	a.sendChat(ctx, strings.Join(parts, "\n"))
}

func (a *Agent) notifySkillDirectiveResult(ctx context.Context, result skillDirectiveResult) {
	parts := []string{}
	if len(result.Skills) > 0 {
		parts = append(parts, "已注入 Skill："+strings.Join(sortedUnique(result.Skills), ", "))
	}
	if len(result.InjectedWrappers) > 0 {
		parts = append(parts, "已注入 Skill 工具："+strings.Join(sortedUnique(result.InjectedWrappers), ", "))
	}
	if len(result.ExistingWrappers) > 0 {
		parts = append(parts, "已存在 Skill 工具："+strings.Join(sortedUnique(result.ExistingWrappers), ", "))
	}
	if len(result.Invalid) > 0 {
		parts = append(parts, "未找到或不可用的 Skill："+strings.Join(sortedUnique(result.Invalid), ", "))
	}
	if len(parts) == 0 {
		return
	}
	a.sendChat(ctx, strings.Join(parts, "\n"))
}
func sortedUnique(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
func (a *Agent) applyInputDirectives(ctx context.Context, row *storage.Session, text string) (toolDirectiveResult, skillDirectiveResult, error) {
	tools := a.prepareToolDirectives(ctx, row, text)
	if tools.Err != nil {
		return toolDirectiveResult{Text: text}, skillDirectiveResult{Text: text}, tools.Err
	}
	skills := a.prepareSkillDirectives(ctx, row, tools.Text)
	if skills.Err != nil {
		return toolDirectiveResult{Text: text}, skillDirectiveResult{Text: text}, skills.Err
	}
	update := toolrun.StateUpdate{
		Tools: append(append([]toolrun.CachedTool(nil), tools.update.Tools...), skills.update.Tools...),
		Tags:  tools.update.Tags, ShownRuleCardFormats: skills.update.ShownRuleCardFormats,
	}
	committed, err := a.commitToolState(ctx, row, update)
	if err != nil {
		return toolDirectiveResult{Text: text}, skillDirectiveResult{Text: text}, err
	}
	toolNames := map[string]bool{}
	skillNames := map[string]bool{}
	for _, item := range tools.update.Tools {
		toolNames[item.Name] = true
	}
	for _, item := range skills.update.Tools {
		skillNames[item.Name] = true
	}
	for _, name := range committed.Injected {
		if toolNames[name] {
			tools.Injected = append(tools.Injected, name)
		}
		if skillNames[name] {
			if toolNames[name] {
				skills.ExistingWrappers = append(skills.ExistingWrappers, name)
			} else {
				skills.InjectedWrappers = append(skills.InjectedWrappers, name)
			}
		}
	}
	for _, name := range committed.Existing {
		if toolNames[name] {
			tools.Existing = append(tools.Existing, name)
		}
		if skillNames[name] {
			skills.ExistingWrappers = append(skills.ExistingWrappers, name)
		}
	}
	return tools, skills, nil
}
