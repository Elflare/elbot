package execution

import (
	"context"
	"log/slog"
	"maps"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/elyph"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/operationresult"
	"elbot/internal/storage"
	toolshare "elbot/internal/tool/share"
)

func (b *runBatch) commit(index int, receipt bool) error {
	task := &b.calls[index]
	result := task.result
	if err := b.ctx.Err(); err != nil {
		return err
	}
	ctx, err := b.deps.RefreshExecution(b.ctx, b.req.Session)
	if err != nil {
		return err
	}
	b.ctx = ctx
	call := task.call
	actor := task.actor

	metadata := storage.SessionMetadata{}
	if err := result.complete(ctx, b.deps, b.req.Session, call, task.risk); err != nil {
		return err
	}
	var rawRecord *storage.ToolCallRecord
	if !receipt {
		rawRecord = result.record(b.sessionID, call, actor, task.risk, task.startedAt, task.finishedAt)
	}
	ruleCard := ""
	if result.Result.Status == operationresult.Success && result.Result.Data != nil {
		toolResult := result.Result.Data
		ruleCard = elyph.RuleCardIfNeeded(toolResult.NeedsElyphRules, b.elyphRulesShown)
		var parts []string
		if toolResult.DiscoveryText != nil && toolResult.DiscoveryText.Summary != "" {
			parts = append(parts, toolResult.DiscoveryText.Summary)
		}
		if ruleCard != "" {
			parts = append(parts, ruleCard)
		}
		if len(parts) > 0 {
			result.Message.Segments = append(llm.TextSegments(strings.Join(parts, "\n\n")+"\n\n"), result.Message.Segments...)
		}
		if call.Name == "discover_tool" {
			update, err := b.manager.DiscoveryStateUpdate(contextinfo.WithActor(ctx, actor), toolResult)
			if err != nil {
				return err
			}
			var discovered []toolshare.DiscoveredTool
			metadata, discovered, err = b.manager.PrepareUpdate(b.req.Session.Metadata, update)
			if err != nil {
				return err
			}
			description, err := b.manager.WeaveDescription(ctx, discovered)
			if err != nil {
				return err
			}
			if description != "" {
				result.Message.Segments = llm.AppendSegmentText(result.Message.Segments, "\n\n"+description)
			}
		}
	}
	stored := b.deps.ToolResultMessage(b.sessionID, result.Message)
	if ruleCard != "" {
		fields, err := storage.DecodeSessionMetadata(stored.Metadata)
		if err != nil {
			return err
		}
		_ = fields.Set("elyph_rules_shown", true)
		stored.Metadata, _ = fields.Encode()
		_ = metadata.Set("elyph_rules_shown", true)
	}
	if err := b.req.Committer.Result(ctx, index, call, result.Message, &storage.ToolResultCommit{Result: &stored, Record: rawRecord, Metadata: metadata}); err != nil {
		return err
	}
	b.deps.PublishToolResult(context.WithoutCancel(ctx), call.ID, delivery.ToolResult{
		Success: result.Error == nil && result.Result.Status == operationresult.Success, Segments: result.Message.Segments,
	})
	task.published = true
	if ruleCard != "" {
		b.elyphRulesShown = true
	}
	if len(metadata) != 0 {
		fields, err := storage.DecodeSessionMetadata(b.req.Session.Metadata)
		if err != nil {
			return err
		}
		maps.Copy(fields, metadata)
		b.req.Session.Metadata, _ = fields.Encode()
	}
	b.completed++
	return nil
}

func (result *executionResult) complete(ctx context.Context, deps toolshare.RunnerDeps, session *storage.Session, call llm.ToolCallRequest, risk string) error {
	deliverOutputs(ctx, deps, result)
	if !result.ExecutionStarted {
		return nil
	}
	completed := operationresult.Result[[]llm.MessageSegment]{Status: result.Result.Status, Message: result.Result.Message}
	if result.Error == nil && completed.Status == operationresult.Success {
		completed.Data = result.Message.Segments
	}
	segments, err := deps.CompleteToolCall(ctx, session, call, risk, completed, result.Error)
	if err == nil {
		result.Message.Segments = segments
	}
	return err
}

func deliverOutputs(ctx context.Context, deps toolshare.RunnerDeps, result *executionResult) {
	if result.Result.Status == operationresult.Success && result.Result.Data != nil && len(result.Result.Data.Outputs) > 0 {
		sent, err := deps.SendOutputs(ctx, result.Result.Data.Outputs)
		if err != nil {
			events.LogAndNotify(ctx, events.LogRecord{Category: events.LogCategoryLog, Level: slog.LevelWarn, EventName: "tool_output_failed", Module: "tool", Content: err.Error(), Identity: events.LogIdentityFromContext(ctx), ResultStatus: events.LogStatus(err)}, events.NewNotification(ctx, slog.LevelWarn, err.Error()))
			result.Error = err
			result.Result = operationresult.Result[*toolshare.Result]{}
			result.Message.Segments = llm.TextSegments(err.Error())
		} else if sent.Status != operationresult.Success {
			result.Result = operationresult.Result[*toolshare.Result]{Status: sent.Status, Message: sent.Message}
			result.Message.Segments = llm.TextSegments(sent.Message)
		}
	}
}

func toolMessage(name, id, content string) llm.LLMMessage {
	return llm.LLMMessage{Role: llm.RoleTool, Name: name, ToolCallID: id, Segments: llm.TextSegments(content)}
}
