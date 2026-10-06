package agent

import (
	"context"
	"sort"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/directive"
	"elbot/internal/security"
	"elbot/internal/session"
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

func (c *inputCoordinator) preloadContext(ctx context.Context) context.Context {
	return contextinfo.WithActor(security.WithPolicy(ctx, c.identity.policy), c.identity.Actor(ctx))
}

func (c *inputCoordinator) prepareToolDirectives(ctx context.Context, row *storage.Session, text string) toolDirectiveResult {
	result := toolDirectiveResult{Text: text}
	if row == nil || row.Mode != storage.SessionModeWork || c.registry == nil || !containsAny(text, directive.ToolPrefix, directive.ToolFullPrefix, directive.ToolShortPrefix, directive.ToolShortFull) {
		return result
	}
	matches := directive.ToolMatches(text)
	if len(matches) == 0 {
		return result
	}
	if !isBackgroundSession(row) {
		ctx = workspace.WithWorkspaceStore(ctx, session.NewWorkspaceStore(c.sessions, c.sessionRows, row.ID))
	}
	names := make([]string, len(matches))
	for i, match := range matches {
		names[i] = match.Name
	}
	prepared, err := c.preloader.PrepareTools(c.preloadContext(ctx), names)
	if err != nil {
		result.Err = err
		return result
	}
	result.Invalid = prepared.Invalid
	if len(prepared.Update.Tools) == 0 {
		return result
	}
	result.update = prepared.Update
	result.Text = directive.StripToolMatches(text, matches, prepared.Accepted)
	if prepared.Content != "" {
		if strings.TrimSpace(result.Text) == "" {
			result.Text = prepared.Content
		} else {
			result.Text = strings.TrimSpace(result.Text) + "\n\n" + prepared.Content
		}
	}
	return result
}

func (c *inputCoordinator) prepareSkillDirectives(ctx context.Context, row *storage.Session, text string) skillDirectiveResult {
	result := skillDirectiveResult{Text: text}
	if row == nil || row.Mode != storage.SessionModeWork || c.registry == nil || !containsAny(text, directive.SkillPrefix, directive.SkillFullPrefix, directive.SkillShortPrefix, directive.SkillShortFull) {
		return result
	}
	matches := directive.SkillMatches(text)
	if len(matches) == 0 {
		return result
	}
	state, err := c.toolState.Snapshot(ctx, row.ID)
	if err != nil {
		result.Err = err
		return result
	}
	ctx = tool.WithShownRuleCardFormats(c.preloadContext(ctx), state.ShownRuleCardFormats)
	names := make([]string, len(matches))
	for i, match := range matches {
		names[i] = match.Name
	}
	prepared := c.preloader.PrepareSkills(ctx, row.ID, names)
	result.Invalid = prepared.Invalid
	if len(prepared.Names) == 0 {
		return result
	}
	result.Skills, result.update = prepared.Names, prepared.Update
	result.Text = directive.StripToolMatches(text, matches, prepared.Accepted)
	if prepared.Content != "" {
		if strings.TrimSpace(result.Text) == "" {
			result.Text = prepared.Content
		} else {
			result.Text = strings.TrimSpace(result.Text) + "\n\n" + prepared.Content
		}
	}
	return result
}

func containsAny(text string, values ...string) bool {
	for _, value := range values {
		if strings.Contains(text, value) {
			return true
		}
	}
	return false
}

func (c *inputCoordinator) notifyToolDirectiveResult(ctx context.Context, result toolDirectiveResult) {
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
	c.output.SendChat(ctx, strings.Join(parts, "\n"))
}

func (c *inputCoordinator) notifySkillDirectiveResult(ctx context.Context, result skillDirectiveResult) {
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
	c.output.SendChat(ctx, strings.Join(parts, "\n"))
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
func (c *inputCoordinator) applyInputDirectives(ctx context.Context, row *storage.Session, text string) (toolDirectiveResult, skillDirectiveResult, error) {
	locked, release, err := c.execution.enterInput(ctx, row)
	if err != nil {
		return toolDirectiveResult{Text: text}, skillDirectiveResult{Text: text}, err
	}
	ctx = locked
	release()
	tools := c.prepareToolDirectives(ctx, row, text)
	if tools.Err != nil {
		return toolDirectiveResult{Text: text}, skillDirectiveResult{Text: text}, tools.Err
	}
	skills := c.prepareSkillDirectives(ctx, row, tools.Text)
	if skills.Err != nil {
		return toolDirectiveResult{Text: text}, skillDirectiveResult{Text: text}, skills.Err
	}
	update := toolrun.StateUpdate{
		Tools: append(append([]toolrun.CachedTool(nil), tools.update.Tools...), skills.update.Tools...),
		Tags:  tools.update.Tags, ShownRuleCardFormats: skills.update.ShownRuleCardFormats,
	}
	locked, release, err = c.execution.enterInput(ctx, row)
	if err != nil {
		return toolDirectiveResult{Text: text}, skillDirectiveResult{Text: text}, err
	}
	committed, err := commitToolState(locked, c.toolState, row, update)
	release()
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
