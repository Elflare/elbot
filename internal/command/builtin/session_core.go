package builtin

import (
	"context"
	"errors"
	"fmt"
	"strings"

	command "elbot/internal/command/share"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/contextmgr"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
	"math"
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

func formatContextData(s command.ContextData) string {
	prefix := contextmgr.FormatTokens(usageForText(s.Usage)) + "\n"
	known := s.Usage != nil && s.Usage.TotalTokens != nil && *s.Usage.TotalTokens >= 0 && s.ContextWindow > 0
	status := "unk"
	if !known {
		prefix += fmt.Sprintf("ctx.window=%d · ratio=unk\n", s.ContextWindow)
	} else {
		ratio := float64(*s.Usage.TotalTokens) / float64(s.ContextWindow)
		prefix += fmt.Sprintf("ctx=%d/%d (%.1f%%)\n", *s.Usage.TotalTokens, s.ContextWindow, ratio*100)
		status = "ok"
		if ratio >= s.CompactTriggerRatio {
			status = "next request"
		} else if ratio >= math.Max(0, s.CompactTriggerRatio-0.1) {
			status = "near"
		}
	}
	if !s.CompactEnabled {
		status = "off"
	}
	return prefix + fmt.Sprintf("compact=%.0f%% · %s\n", s.CompactTriggerRatio*100, status)
}

func formatToolUsage(usage, live map[string]int) string {
	persisted, current := toolsString(usage), toolsString(live)
	var sb strings.Builder
	if persisted != "" {
		fmt.Fprintf(&sb, "tools.saved: %s\n", persisted)
	}
	if current != "" {
		fmt.Fprintf(&sb, "tools.turn: %s\n", current)
	}
	return sb.String()
}

func tokenUsageData(usage *llm.Usage) *command.TokenUsage {
	if usage == nil {
		return nil
	}
	return &command.TokenUsage{PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens, TotalTokens: usage.TotalTokens, CacheHitTokens: usage.CacheHitTokens}
}

func usageForText(usage *command.TokenUsage) *llm.Usage {
	if usage == nil {
		return nil
	}
	return &llm.Usage{PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens, TotalTokens: usage.TotalTokens, CacheHitTokens: usage.CacheHitTokens}
}
