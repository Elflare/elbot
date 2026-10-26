package agent

import (
	"context"
	"elbot/internal/events"
	"elbot/internal/fileops"
	"elbot/internal/llm"
	"elbot/internal/tool"
	toolshare "elbot/internal/tool/share"
	toolstate "elbot/internal/tool/state"
)

func (c isolatedToolCapabilities) DecodeState(raw string) (tool.State, error) {
	s, e := toolstate.DecodeState(raw)
	return isolated_toPublicState(s), e
}
func (c isolatedToolCapabilities) Schemas(ctx context.Context, tc tool.Context, cached []tool.DiscoveredTool) ([]llm.ToolSchema, error) {
	return c.Manager.Schemas(ctx, toolshare.Context(tc), isolated_toInternalDiscoveredTools(cached))
}
func (c isolatedToolCapabilities) List() []tool.Info {
	return isolated_toPublicInfos(c.Registry.List())
}
func (c isolatedToolCapabilities) ToolInfo(name string) (tool.Info, bool) {
	i, ok := c.Registry.ToolInfo(name)
	return isolated_toPublicInfo(i), ok
}
func (c isolatedToolCapabilities) QueryTools(ctx context.Context, q tool.ToolQuery) ([]tool.Info, error) {
	i, e := c.Registry.QueryToolInfos(ctx, toolshare.ToolQuery(q))
	return isolated_toPublicInfos(i), e
}
func (c isolatedToolCapabilities) ToolTags(ctx context.Context) ([]string, error) {
	return c.PreloadService.Tags(ctx)
}
func (c isolatedToolCapabilities) ToolNamesByTag(ctx context.Context, tag string, q tool.ToolQuery) ([]string, error) {
	return c.PreloadService.ToolNamesByTag(ctx, tag, toolshare.ToolQuery(q))
}
func (c isolatedToolCapabilities) Run(ctx context.Context, d tool.RunnerDeps, r tool.RunRequest) tool.RunResult {
	return tool.RunResult(c.Manager.Run(ctx, isolatedExecutionBridge{d}, isolated_toInternalRunRequest(r)))
}
func (c isolatedToolCapabilities) DiscoveredToolsForSession(ctx context.Context, id string, bg bool) ([]tool.DiscoveredTool, events.LogResult, error) {
	v, l, e := c.StateService.DiscoveredToolsForSession(ctx, id, bg)
	return isolated_toPublicDiscoveredTools(v), l, e
}
func (c isolatedToolCapabilities) Commit(ctx context.Context, id string, u tool.StateUpdate) (tool.StateCommit, events.LogResult, error) {
	v, l, e := c.StateService.Commit(ctx, id, isolated_toInternalStateUpdate(u))
	return isolated_toPublicStateCommit(v), l, e
}
func (c isolatedToolCapabilities) PrepareTools(ctx context.Context, n []string) (tool.ToolPreload, error) {
	v, e := c.PreloadService.PrepareTools(ctx, n)
	return isolated_toPublicToolPreload(v), e
}

func (c isolatedToolCapabilities) PrepareBackground(ctx context.Context, req tool.BackgroundPreloadRequest) (tool.BackgroundPreload, error) {
	declarations := make([]toolshare.ELwispToolDeclaration, len(req.ELwispTools))
	for i, declaration := range req.ELwispTools {
		declarations[i] = toolshare.ELwispToolDeclaration(declaration)
	}
	prepared, err := c.PreloadService.PrepareBackground(ctx, toolshare.BackgroundPreloadRequest{
		SessionID: req.SessionID, Names: req.Names, AllowedToolNames: req.AllowedToolNames, ELwispTools: declarations,
	})
	return tool.BackgroundPreload{NeedsElyphRules: prepared.NeedsElyphRules, Update: isolated_toPublicStateUpdate(prepared.Update), Skills: prepared.Skills, SkillPrompt: prepared.SkillPrompt}, err
}
func (c isolatedToolCapabilities) PrepareSkills(ctx context.Context, p string, n []string) (tool.SkillPreload, error) {
	v, e := c.PreloadService.PrepareSkills(ctx, p, n)
	return isolated_toPublicSkillPreload(v), e
}
func (c isolatedToolCapabilities) TagPrompts(ctx context.Context, n []string) ([]tool.TagPrompt, error) {
	v, e := c.PreloadService.TagPrompts(ctx, n)
	return isolated_toPublicTagPrompts(v), e
}
func (c isolatedToolCapabilities) ListRollbacks(ctx context.Context) ([]fileops.RollbackInfo, error) {
	return c.Service.List(ctx)
}
func (c isolatedToolCapabilities) Forget(b fileops.Binding) { c.Service.Manager.Forget(b) }
func (c isolatedToolCapabilities) Remove(ctx context.Context, n string) error {
	return c.skills.Remove(ctx, n)
}
func (c isolatedToolCapabilities) Reload(ctx context.Context) error { return c.skills.Reload(ctx) }

// executionBridge 将本次调用借用的公开回调转换成执行器的内部契约。
type isolatedExecutionBridge struct{ tool.RunnerDeps }

func (b isolatedExecutionBridge) ConfirmToolCall(ctx context.Context, id string, call llm.ToolCallRequest, assessment toolshare.RiskAssessment, detail string) (toolshare.ConfirmResult, error) {
	result, err := b.RunnerDeps.ConfirmToolCall(ctx, id, call, tool.RiskAssessment(assessment), detail)
	return toolshare.ConfirmResult(result), err
}

func isolated_toPublicInfo(info toolshare.Info) tool.Info {
	return tool.Info{NeedsElyphRules: info.NeedsElyphRules, APITypes: info.APITypes, Name: info.Name,
		Description: info.Description, Source: tool.Source(info.Source), Risk: info.Risk,
		SuperadminOnly: info.SuperadminOnly, Hidden: info.Hidden, OwnerScoped: info.OwnerScoped,
		ForegroundOnly: info.ForegroundOnly, Tags: info.Tags, DependsOn: info.DependsOn}
}

func isolated_toPublicInfos(infos []toolshare.Info) []tool.Info {
	if infos == nil {
		return nil
	}
	out := make([]tool.Info, len(infos))
	for i, info := range infos {
		out[i] = isolated_toPublicInfo(info)
	}
	return out
}

func isolated_toPublicDiscoveredTools(items []toolshare.DiscoveredTool) []tool.DiscoveredTool {
	if items == nil {
		return nil
	}
	out := make([]tool.DiscoveredTool, len(items))
	for i, item := range items {
		out[i] = tool.DiscoveredTool{Name: item.Name, CanonicalName: item.CanonicalName,
			Source: tool.SourceKind(item.Source), Description: item.Description, Schema: item.Schema,
			EventKey: item.EventKey, Endpoint: item.Endpoint, TimeoutSeconds: item.TimeoutSeconds,
			ForegroundOnly: item.ForegroundOnly}
	}
	return out
}

func isolated_toInternalDiscoveredTools(items []tool.DiscoveredTool) []toolshare.DiscoveredTool {
	if items == nil {
		return nil
	}
	out := make([]toolshare.DiscoveredTool, len(items))
	for i, item := range items {
		out[i] = toolshare.DiscoveredTool{Name: item.Name, CanonicalName: item.CanonicalName,
			Source: toolshare.SourceKind(item.Source), Description: item.Description, Schema: item.Schema,
			EventKey: item.EventKey, Endpoint: item.Endpoint, TimeoutSeconds: item.TimeoutSeconds,
			ForegroundOnly: item.ForegroundOnly}
	}
	return out
}

func isolated_toPublicState(state toolshare.State) tool.State {
	return tool.State{DiscoveredTools: isolated_toPublicDiscoveredTools(state.DiscoveredTools), ToolTags: state.ToolTags, ElyphRulesShown: state.ElyphRulesShown}
}

func isolated_toInternalStateUpdate(update tool.StateUpdate) toolshare.StateUpdate {
	return toolshare.StateUpdate{Tools: isolated_toInternalDiscoveredTools(update.Tools), Tags: update.Tags}
}

func isolated_toPublicStateUpdate(update toolshare.StateUpdate) tool.StateUpdate {
	return tool.StateUpdate{Tools: isolated_toPublicDiscoveredTools(update.Tools), Tags: update.Tags}
}

func isolated_toPublicStateCommit(committed toolshare.StateCommit) tool.StateCommit {
	return tool.StateCommit{State: isolated_toPublicState(committed.State), Metadata: committed.Metadata, Injected: committed.Injected, Existing: committed.Existing}
}

func isolated_toPublicToolPreload(prepared toolshare.ToolPreload) tool.ToolPreload {
	return tool.ToolPreload{NeedsElyphRules: prepared.NeedsElyphRules, Update: isolated_toPublicStateUpdate(prepared.Update), Accepted: prepared.Accepted, Invalid: prepared.Invalid, Content: prepared.Content}
}

func isolated_toPublicSkillPreload(prepared toolshare.SkillPreload) tool.SkillPreload {
	return tool.SkillPreload{NeedsElyphRules: prepared.NeedsElyphRules, Update: isolated_toPublicStateUpdate(prepared.Update), Accepted: prepared.Accepted, Names: prepared.Names, Invalid: prepared.Invalid, Content: prepared.Content}
}

func isolated_toPublicTagPrompts(items []toolshare.TagPrompt) []tool.TagPrompt {
	if items == nil {
		return nil
	}
	out := make([]tool.TagPrompt, len(items))
	for i, item := range items {
		out[i] = tool.TagPrompt(item)
	}
	return out
}

func isolated_toInternalRunRequest(req tool.RunRequest) toolshare.RunRequest {
	return toolshare.RunRequest{Committer: req.Committer, Session: req.Session, Calls: req.Calls, AssistantText: req.AssistantText, AssistantRawText: req.AssistantRawText, DiscoveredTools: isolated_toInternalDiscoveredTools(req.DiscoveredTools), Actor: req.Actor}
}
