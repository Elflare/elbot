// 适配 Tool 公开契约与内部组件使用的类型和回调。
package tool

import (
	"context"

	"elbot/internal/llm"
	"elbot/internal/operationresult"
	"elbot/internal/storage"
	"elbot/internal/tool/share"
)

// executionBridge 将本次调用借用的公开回调转换成执行器的内部契约。
type executionBridge struct{ RunnerDeps }

func (b executionBridge) RefreshExecution(ctx context.Context, row *storage.Session) (context.Context, error) {
	ctx, err := b.RunnerDeps.RefreshExecution(ctx, row)
	if err != nil {
		return ctx, err
	}
	return toolPolicyContext(ctx), nil
}

func (b executionBridge) ConfirmToolCall(ctx context.Context, id string, call llm.ToolCallRequest, assessment share.RiskAssessment, detail []share.ConfirmationDetailBlock) (operationresult.Result[share.ConfirmResult], error) {
	result, err := b.RunnerDeps.ConfirmToolCall(ctx, id, call, RiskAssessment(assessment), detail)
	if err != nil {
		return operationresult.Result[share.ConfirmResult]{}, err
	}
	adapted := operationresult.Result[share.ConfirmResult]{Status: result.Status, Message: result.Message}
	if result.Status == operationresult.Success {
		adapted.Data = share.ConfirmResult(result.Data)
	}
	return adapted, nil
}

func toInternalToolQuery(query ToolQuery) share.ToolQuery {
	return share.ToolQuery{Scope: share.QueryScope(query.Scope), Kind: query.Kind, Names: query.Names, IncludeHidden: query.IncludeHidden, IncludeDiscover: query.IncludeDiscover}
}

func toPublicDiscoveredTools(items []share.DiscoveredTool) []DiscoveredTool {
	if items == nil {
		return nil
	}
	out := make([]DiscoveredTool, len(items))
	for i, item := range items {
		out[i] = DiscoveredTool(item)
	}
	return out
}

func toInternalDiscoveredTools(items []DiscoveredTool) []share.DiscoveredTool {
	if items == nil {
		return nil
	}
	out := make([]share.DiscoveredTool, len(items))
	for i, item := range items {
		out[i] = share.DiscoveredTool(item)
	}
	return out
}

func toPublicState(state share.State) State {
	return State{DiscoveredTools: toPublicDiscoveredTools(state.DiscoveredTools), ToolTags: state.ToolTags, ElyphRulesShown: state.ElyphRulesShown}
}

func toInternalStateUpdate(update StateUpdate) share.StateUpdate {
	groups := make([]share.ToolRegistrationScope, len(update.ReplaceGroups))
	for i, group := range update.ReplaceGroups {
		groups[i] = share.ToolRegistrationScope(group)
	}
	return share.StateUpdate{Tools: toInternalDiscoveredTools(update.Tools), Tags: update.Tags, ReplaceGroups: groups}
}

func toPublicStateUpdate(update share.StateUpdate) StateUpdate {
	groups := make([]ToolRegistrationScope, len(update.ReplaceGroups))
	for i, group := range update.ReplaceGroups {
		groups[i] = ToolRegistrationScope(group)
	}
	return StateUpdate{Tools: toPublicDiscoveredTools(update.Tools), Tags: update.Tags, ReplaceGroups: groups}
}

func toPublicStateCommit(committed share.StateCommit) StateCommit {
	return StateCommit{Metadata: committed.Metadata, Injected: committed.Injected, Existing: committed.Existing}
}

func toPublicToolPreload(prepared share.ToolPreload) ToolPreload {
	return ToolPreload{NeedsElyphRules: prepared.NeedsElyphRules, Update: toPublicStateUpdate(prepared.Update), Accepted: prepared.Accepted, Invalid: prepared.Invalid, Content: prepared.Content}
}

func toPublicSkillPreload(prepared share.SkillPreload) SkillPreload {
	return SkillPreload{NeedsElyphRules: prepared.NeedsElyphRules, Update: toPublicStateUpdate(prepared.Update), Accepted: prepared.Accepted, Names: prepared.Names, Invalid: prepared.Invalid, Content: prepared.Content}
}

func toInternalBackgroundPreloadRequest(req BackgroundPreloadRequest) share.BackgroundPreloadRequest {
	var declarations []share.ELwispToolDeclaration
	if req.ELwispTools != nil {
		declarations = make([]share.ELwispToolDeclaration, len(req.ELwispTools))
		for i, declaration := range req.ELwispTools {
			declarations[i] = share.ELwispToolDeclaration(declaration)
		}
	}
	return share.BackgroundPreloadRequest{SessionID: req.SessionID, Names: req.Names, AllowedToolNames: req.AllowedToolNames, ELwispTools: declarations}
}

func toPublicBackgroundPreload(prepared share.BackgroundPreload) BackgroundPreload {
	return BackgroundPreload{NeedsElyphRules: prepared.NeedsElyphRules, Update: toPublicStateUpdate(prepared.Update), Skills: prepared.Skills, SkillPrompt: prepared.SkillPrompt}
}

func toPublicBackgroundSelections(items []share.BackgroundSelection) []BackgroundSelection {
	if items == nil {
		return nil
	}
	out := make([]BackgroundSelection, len(items))
	for i, item := range items {
		out[i] = BackgroundSelection(item)
	}
	return out
}

func toPublicTagPrompts(items []share.TagPrompt) []TagPrompt {
	if items == nil {
		return nil
	}
	out := make([]TagPrompt, len(items))
	for i, item := range items {
		out[i] = TagPrompt(item)
	}
	return out
}

func toInternalRunRequest(req RunRequest) share.RunRequest {
	return share.RunRequest{Jobs: req.Jobs, Committer: req.Committer, Session: req.Session, Calls: req.Calls, AssistantText: req.AssistantText, AssistantRawText: req.AssistantRawText, ReasoningContent: req.ReasoningContent, DiscoveredTools: toInternalDiscoveredTools(req.DiscoveredTools), Actor: req.Actor}
}

func toPublicResult(result *share.Result) *Result {
	if result == nil {
		return nil
	}
	return &Result{Summary: result.Summary, NeedsElyphRules: result.NeedsElyphRules, DiscoveryText: (*DiscoveryText)(result.DiscoveryText), Content: result.Content, Segments: result.Segments, Warnings: result.Warnings, Data: result.Data, Metadata: result.Metadata, Outputs: result.Outputs}
}

func toInternalResult(result *Result) *share.Result {
	if result == nil {
		return nil
	}
	return &share.Result{Summary: result.Summary, NeedsElyphRules: result.NeedsElyphRules, DiscoveryText: (*share.DiscoveryText)(result.DiscoveryText), Content: result.Content, Segments: result.Segments, Warnings: result.Warnings, Data: result.Data, Metadata: result.Metadata, Outputs: result.Outputs}
}
