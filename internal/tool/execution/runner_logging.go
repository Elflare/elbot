// 组装并发布批次编排与工具调用日志，不修改业务结果。
package execution

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/llm"
	toolshare "elbot/internal/tool/share"
)

func newRunRecord(ctx context.Context, sessionID, eventName string, err error) events.LogRecord {
	level, status, content := slog.LevelInfo, events.ResultSucceeded, ""
	if err != nil {
		level, status, content = slog.LevelWarn, events.ResultFailed, err.Error()
		var denied toolshare.PolicyDeniedError
		if errors.As(err, &denied) {
			status = events.ResultRejected
		}
		if events.LogStatus(err) == events.ResultCanceled {
			level, status = slog.LevelInfo, events.ResultCanceled
		}
	}
	record := events.LogRecord{Category: events.LogRuntime, Level: level, EventName: eventName, Module: "toolrun", Content: content, ContentLimit: 4096, ResultStatus: status}
	// Read the current operation's execution facts after any foreground handoff.
	// SessionID belongs to the supplied Session, including an explicit empty ID.
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	record.Identity = events.LogIdentityFromExecution(facts)
	record.Identity.SessionID = sessionID
	return record
}

func (m *Manager) recordCallStarted(ctx context.Context, sessionID string, call llm.ToolCallRequest, source toolshare.SourceKind, actor contextinfo.Actor, risk, runSource string) {
	content, _ := json.Marshal(map[string]any{"tool": call.Name, "tool_call_id": call.ID, "actor": actor, "arguments": toolshare.TruncateLogContent(call.Arguments, 512), "risk": risk, "run_source": runSource, "source": string(source)})
	record := newRunRecord(ctx, sessionID, "tool_call_prepared", nil)
	record.Category, record.Type, record.ContentLimit, record.Content = events.LogAudit, events.ContentJSON, 0, string(content)
	m.Log(ctx, record)
}

func (m *Manager) recordCallCompleted(ctx context.Context, sessionID string, call llm.ToolCallRequest, source toolshare.SourceKind, actor contextinfo.Actor, risk, runSource string, startedAt time.Time, result executionResult) {
	// The execution source declares the outcome; tools supply their diagnostic body.
	// runSource was fixed before entry; source separately identifies the
	// native/Elwisp implementation. Neither is inferred from diagnostics.
	// Keep the source-owned summary separate from the bounded actual result.
	content := struct {
		Tool       string            `json:"tool"`
		Actor      contextinfo.Actor `json:"actor"`
		ElapsedMS  int64             `json:"elapsed_ms"`
		Summary    string            `json:"summary,omitempty"` // Source-owned summary, preserved without truncation.
		Result     string            `json:"result"`
		Risk       string            `json:"risk"`
		RunSource  string            `json:"run_source"`
		Source     string            `json:"source"`
		ToolCallID string            `json:"tool_call_id"`
	}{
		Tool:       toolshare.TruncateLogContent(call.Name, 2048),
		Actor:      actor,
		ElapsedMS:  time.Since(startedAt).Milliseconds(),
		Summary:    result.Summary,
		Result:     result.Result.LogContent(result.Error),
		Risk:       toolshare.TruncateLogContent(risk, 2048),
		RunSource:  toolshare.TruncateLogContent(runSource, 2048),
		Source:     toolshare.TruncateLogContent(string(source), 2048),
		ToolCallID: toolshare.TruncateLogContent(call.ID, 2048),
	}
	data, _ := json.Marshal(content)
	record := newRunRecord(ctx, sessionID, "tool_call_completed", result.Error)
	record.Category, record.Type, record.ContentLimit, record.Content = events.LogAudit, events.ContentJSON, 0, string(data)
	m.Log(ctx, record)
}
