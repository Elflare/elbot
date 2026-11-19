package input

import (
	"context"
	"elbot/internal/delivery"
	"elbot/internal/operationresult"
	"elbot/internal/tool"
	"sort"
	"strings"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/directive"
	"elbot/internal/storage"

	"elbot/internal/workspace"
)

type toolDirectiveResult struct {
	WeaveDescription string // 当前预载结果中的可编排名单。
	NeedsElyphRules  bool
	update           tool.StateUpdate
	Text             string
	Injected         []string
	Existing         []string
	Invalid          []string
}

type skillDirectiveResult struct {
	NeedsElyphRules  bool
	update           tool.StateUpdate
	Text             string
	Skills           []string
	InjectedWrappers []string
	ExistingWrappers []string
	Invalid          []string
}

func (c *InputCoordinator) prepareToolDirectives(ctx context.Context, row *storage.Session, text string) (toolDirectiveResult, error) {
	result := toolDirectiveResult{Text: text}
	if row == nil || row.Mode != storage.SessionModeWork || !containsAny(text, directive.ToolPrefix, directive.ToolFullPrefix, directive.ToolShortPrefix, directive.ToolShortFull) {
		return result, nil
	}
	matches := directive.ToolMatches(text)
	if len(matches) == 0 {
		return result, nil
	}
	if !agentshare.IsBackgroundSession(row) {
		ctx = workspace.WithWorkspaceStore(ctx, c.deps.NewWorkspaceStore(row.ID))
	}
	names := make([]string, len(matches))
	for i, match := range matches {
		names[i] = match.Name
	}
	prepared, err := c.deps.Preloader.PrepareTools(ctx, names)
	if err != nil {
		return toolDirectiveResult{}, err
	}
	result.Invalid = prepared.Invalid
	if len(prepared.Update.Tools) == 0 && prepared.Content == "" {
		return result, nil
	}
	result.update = prepared.Update
	result.NeedsElyphRules = prepared.NeedsElyphRules
	result.Text = directive.StripToolMatches(text, matches, prepared.Accepted)
	result.Text = appendDirectiveContent(result.Text, prepared.Content)
	return result, nil
}

func (c *InputCoordinator) prepareSkillDirectives(ctx context.Context, row *storage.Session, text string) (skillDirectiveResult, error) {
	result := skillDirectiveResult{Text: text}
	if row == nil || row.Mode != storage.SessionModeWork || !containsAny(text, directive.SkillPrefix, directive.SkillFullPrefix, directive.SkillShortPrefix, directive.SkillShortFull) {
		return result, nil
	}
	matches := directive.SkillMatches(text)
	if len(matches) == 0 {
		return result, nil
	}
	names := make([]string, len(matches))
	for i, match := range matches {
		names[i] = match.Name
	}
	prepared, err := c.deps.Preloader.PrepareSkills(ctx, row.ID, names)
	if err != nil {
		return skillDirectiveResult{}, err
	}
	result.Invalid = prepared.Invalid
	if len(prepared.Names) == 0 {
		return result, nil
	}
	result.Skills, result.update = prepared.Names, prepared.Update
	result.NeedsElyphRules = prepared.NeedsElyphRules
	result.Text = directive.StripToolMatches(text, matches, prepared.Accepted)
	result.Text = appendDirectiveContent(result.Text, prepared.Content)
	return result, nil
}

func appendDirectiveContent(text, content string) string {
	if content == "" {
		return text
	}
	if text = strings.TrimSpace(text); text == "" {
		return content
	}
	return text + "\n\n" + content
}

func containsAny(text string, values ...string) bool {
	for _, value := range values {
		if strings.Contains(text, value) {
			return true
		}
	}
	return false
}

func (c *InputCoordinator) notifyToolDirectiveResult(ctx context.Context, result toolDirectiveResult) error {
	parts := []string{}
	if len(result.Injected) > 0 {
		parts = append(parts, "已加载: "+strings.Join(sortedUnique(result.Injected), ", "))
	}
	if len(result.Existing) > 0 {
		parts = append(parts, "已存在: "+strings.Join(sortedUnique(result.Existing), ", "))
	}
	if len(result.Invalid) > 0 {
		parts = append(parts, "tools unavailable: "+strings.Join(sortedUnique(result.Invalid), ", "))
	}
	if result.WeaveDescription != "" {
		parts = append(parts, result.WeaveDescription)
	}
	if len(parts) == 0 {
		return nil
	}
	_, err := c.deps.Output.Send(ctx, delivery.StreamEvent{Type: delivery.StreamSend, Text: strings.Join(parts, "\n")})
	return err
}

func (c *InputCoordinator) notifySkillDirectiveResult(ctx context.Context, result skillDirectiveResult) error {
	parts := []string{}
	if len(result.Skills) > 0 {
		parts = append(parts, "已加载: "+strings.Join(sortedUnique(result.Skills), ", "))
	}
	if len(result.InjectedWrappers) > 0 {
		parts = append(parts, "已加载: "+strings.Join(sortedUnique(result.InjectedWrappers), ", "))
	}
	if len(result.ExistingWrappers) > 0 {
		parts = append(parts, "已存在: "+strings.Join(sortedUnique(result.ExistingWrappers), ", "))
	}
	if len(result.Invalid) > 0 {
		parts = append(parts, "skills unavailable: "+strings.Join(sortedUnique(result.Invalid), ", "))
	}
	if len(parts) == 0 {
		return nil
	}
	_, err := c.deps.Output.Send(ctx, delivery.StreamEvent{Type: delivery.StreamSend, Text: strings.Join(parts, "\n")})
	return err
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

func (c *InputCoordinator) applyInputDirectives(ctx context.Context, row *storage.Session, text string) (context.Context, operationresult.Result[toolDirectiveResult], skillDirectiveResult, error) {
	locked, admitted, release, err := c.deps.Execution.EnterInput(ctx, row)
	if err != nil {
		return ctx, operationresult.Result[toolDirectiveResult]{}, skillDirectiveResult{}, err
	}
	if admitted.Status != operationresult.Success {
		return ctx, operationresult.Result[toolDirectiveResult]{Status: admitted.Status, Message: admitted.Message}, skillDirectiveResult{}, nil
	}
	ctx = locked
	release()
	ctx, err = c.deps.Execution.ModelContext(ctx, row)
	if err != nil {
		return ctx, operationresult.Result[toolDirectiveResult]{}, skillDirectiveResult{}, err
	}
	ctx, err = c.deps.Preloader.PrepareAvailabilityContext(ctx)
	if err != nil {
		return ctx, operationresult.Result[toolDirectiveResult]{}, skillDirectiveResult{}, err
	}
	tools, err := c.prepareToolDirectives(ctx, row, text)
	if err != nil {
		return ctx, operationresult.Result[toolDirectiveResult]{}, skillDirectiveResult{}, err
	}
	skills, err := c.prepareSkillDirectives(ctx, row, tools.Text)
	if err != nil {
		return ctx, operationresult.Result[toolDirectiveResult]{}, skillDirectiveResult{}, err
	}
	update := tool.StateUpdate{
		Tools: append(append([]tool.DiscoveredTool(nil), tools.update.Tools...), skills.update.Tools...),
		Tags:  tools.update.Tags,
	}
	locked, admitted, release, err = c.deps.Execution.EnterInput(ctx, row)
	if err != nil {
		return ctx, operationresult.Result[toolDirectiveResult]{}, skillDirectiveResult{}, err
	}
	if admitted.Status != operationresult.Success {
		return ctx, operationresult.Result[toolDirectiveResult]{Status: admitted.Status, Message: admitted.Message}, skillDirectiveResult{}, nil
	}
	committed, err := commitToolState(locked, c.deps.ToolState, row, update)
	release()
	if err != nil {
		return ctx, operationresult.Result[toolDirectiveResult]{}, skillDirectiveResult{}, err
	}
	toolNames := map[string]bool{}
	tools.WeaveDescription = committed.WeaveDescription
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
	return ctx, operationresult.Result[toolDirectiveResult]{Status: operationresult.Success, Data: tools}, skills, nil
}

func commitToolState(ctx context.Context, state interface {
	Commit(context.Context, string, tool.StateUpdate) (tool.StateCommit, error)
}, row *storage.Session, update tool.StateUpdate) (tool.StateCommit, error) {
	if len(update.Tools) == 0 && len(update.Tags) == 0 {
		return tool.StateCommit{}, nil
	}
	result, err := state.Commit(ctx, row.ID, update)
	if err != nil {
		return tool.StateCommit{}, err
	}
	row.Metadata = result.Metadata
	return result, nil
}
