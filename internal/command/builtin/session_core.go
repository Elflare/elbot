package builtin

import (
	"context"
	"errors"

	command "elbot/internal/command/share"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/contextmgr"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

func NewNew(deps Deps) command.Handler {
	return command.NewFunc(command.Info{
		Name: "new", Description: "重置会话", SessionEffect: command.SessionEffectSwitchCurrent, MinRole: contextinfo.RoleUser,
		Help: "解除当前绑定；下次对话新建，旧会话保留",
	}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		if err := deps.Sessions.ResetCurrent(ctx, deps.Scope(ctx)); err != nil {
			return nil, err
		}
		return sessionChange(ctx, deps, nil, "", 0, "new"), nil
	})
}

func NewStatus(deps Deps) command.Handler {
	return command.NewFunc(command.Info{
		Name: "status", Description: "当前会话状态", MinRole: contextinfo.RoleUser,
	}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		status, err := deps.Sessions.Status(ctx, deps.Scope(ctx))
		if errors.Is(err, session.ErrNoCurrentSession) {
			mode := deps.Sessions.DefaultMode()
			model := deps.Models.CurrentModelForMode(mode)
			scope := deps.Scope(ctx)
			compact := deps.Models.CurrentCompactModel(mode)
			state := command.StatusData{Mode: mode, Model: modelData(model), Phase: turn.PhaseIdle, Requests: []command.RequestData{}, ToolUsage: map[string]int{}, Errors: []command.ItemError{}, DefaultMode: mode, CompactModel: modelData(compact), Platform: scope.Platform, ChatID: scope.ChatID}
			if deps.Contexts != nil {
				v := deps.Contexts.Status(ctx, nil, config.ModelSelection{Provider: model.Provider, Model: model.Model})
				state.Context = contextData(v)
			}
			return &command.Result{Kind: command.ResultKindStatus, Data: state}, nil
		}
		if err != nil {
			return nil, err
		}
		s := status.Session
		requests := deps.Requests.ListBySession(s.ID)
		turnSnapshot := deps.Turns.Snapshot(s.ID)
		currentModel := deps.Models.CurrentModelForMode(s.Mode)
		contextState, contextErrors := loadContextStatus(ctx, deps, s, currentModel)
		toolUsage := map[string]int{}
		if deps.Store != nil && deps.Store.ToolCalls() != nil {
			summaries, err := deps.Store.ToolCalls().UsageBySession(ctx, s.ID)
			if err != nil {
				toolUsage = nil
				contextErrors = append(contextErrors, command.ItemError{ID: "tool_usage", Error: command.ClassifyError(err)})
			} else {
				for _, entry := range summaries {
					toolUsage[entry.ToolName] += entry.Count
				}
			}
		}
		data := command.StatusData{Session: sessionData(s), Mode: s.Mode, Model: modelData(currentModel), Phase: turnSnapshot.Phase, PendingCount: turnSnapshot.PendingCount, MessageCount: status.MessageCount, LastUserContent: status.LastUserContent, LastAssistantContent: status.LastAnswerContent, Requests: requestData(ctx, deps, requests), ToolUsage: toolUsage, TurnToolUsage: turnSnapshot.Tools, Context: contextState, Errors: contextErrors}
		return &command.Result{Kind: command.ResultKindStatus, Data: data}, nil
	})
}

func loadContextStatus(ctx context.Context, deps Deps, row *storage.Session, model modelmgr.ModelOption) (command.ContextData, []command.ItemError) {
	failures := []command.ItemError{}
	if deps.Contexts == nil {
		return command.ContextData{}, failures
	}
	usage, err := deps.Usage(row)
	if err != nil {
		failures = append(failures, command.ItemError{ID: "usage", Error: command.ClassifyError(err)})
	}
	state := deps.Contexts.Status(ctx, usage, config.ModelSelection{Provider: model.Provider, Model: model.Model})
	return contextData(state), failures
}

func contextData(s contextmgr.UsageState) command.ContextData {
	return command.ContextData{Usage: tokenUsageData(s.Usage), ContextWindow: s.ContextWindow, CompactEnabled: s.CompactEnabled, CompactTriggerRatio: s.TriggerRatio}
}

func tokenUsageData(usage *llm.Usage) *command.TokenUsage {
	if usage == nil {
		return nil
	}
	return &command.TokenUsage{PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens, TotalTokens: usage.TotalTokens, CacheHitTokens: usage.CacheHitTokens}
}
