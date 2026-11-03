// 组装并发布批次编排与工具调用日志，不修改业务结果。
package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/llm"
	toolshare "elbot/internal/tool/share"
)

func newRunRecord(ctx context.Context, sessionID, eventName string, logResult events.LogResult) events.LogRecord {
	level := slog.LevelInfo
	if logResult.ResultStatus == events.ResultFailed {
		level = slog.LevelError
	}
	record := events.LogRecord{Category: events.LogRuntime, Level: level, EventName: eventName, Module: "toolrun", Type: events.ContentText, Content: logResult.Content, ContentLimit: 4096, Identity: events.LogIdentity{SessionID: &sessionID}, ResultStatus: logResult.ResultStatus}
	// Read the current operation's execution facts after any foreground handoff.
	// SessionID belongs to the supplied Session, including an explicit empty ID.
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	record.Identity.RunID = &facts.RunID
	record.Identity.Attempt = &facts.Attempt
	record.Identity.RequestID = &facts.RequestID
	record.Identity.RootRequestID = &facts.RootRequestID
	return record
}

func (m *Manager) recordOperation(ctx context.Context, sessionID string, call llm.ToolCallRequest, eventName string, logResult events.LogResult) {
	record := newRunRecord(ctx, sessionID, eventName, logResult)
	record.Content = fmt.Sprintf("tool=%s tool_call_id=%s\n%s", call.Name, call.ID, logResult.Content)
	m.Log(ctx, record)
}

func (m *Manager) recordCallStarted(ctx context.Context, sessionID string, call llm.ToolCallRequest, source toolshare.SourceKind, actor contextinfo.Actor, risk, runSource string) {
	content, _ := json.Marshal(map[string]string{"tool": call.Name, "tool_call_id": call.ID, "actor_id": actor.ID, "arguments": toolshare.TruncateLogContent(call.Arguments, 512), "risk": risk, "run_source": runSource, "source": string(source)})
	record := newRunRecord(ctx, sessionID, "tool_call_prepared", events.LogResult{ResultStatus: events.ResultSucceeded})
	record.Category, record.Type, record.ContentLimit, record.Content = events.LogAudit, events.ContentJSON, 0, string(content)
	m.Log(ctx, record)
}

func (m *Manager) recordCallCompleted(ctx context.Context, sessionID string, call llm.ToolCallRequest, source toolshare.SourceKind, actor contextinfo.Actor, risk, runSource string, startedAt time.Time, result executionResult) {
	// The execution source declares the outcome; tools supply their diagnostic body.
	// runSource was fixed before entry; source separately identifies the
	// native/Elwisp implementation. Neither is inferred from diagnostics.
	logResult := result.LogResult
	logResult.ResultStatus = result.ResultStatus
	// Keep the source-owned summary separate from the bounded actual result.
	content := struct {
		Tool       string `json:"tool"`
		ActorID    string `json:"actor_id"`
		ElapsedMS  int64  `json:"elapsed_ms"`
		Summary    string `json:"summary,omitempty"` // Source-owned summary, preserved without truncation.
		Result     string `json:"result"`
		Risk       string `json:"risk"`
		RunSource  string `json:"run_source"`
		Source     string `json:"source"`
		ToolCallID string `json:"tool_call_id"`
	}{
		Tool:       toolshare.TruncateLogContent(call.Name, 2048),
		ActorID:    toolshare.TruncateLogContent(actor.ID, 2048),
		ElapsedMS:  time.Since(startedAt).Milliseconds(),
		Summary:    logResult.Content,
		Result:     result.Result.LogContent(result.Error),
		Risk:       toolshare.TruncateLogContent(risk, 2048),
		RunSource:  toolshare.TruncateLogContent(runSource, 2048),
		Source:     toolshare.TruncateLogContent(string(source), 2048),
		ToolCallID: toolshare.TruncateLogContent(call.ID, 2048),
	}
	data, _ := json.Marshal(content)
	record := newRunRecord(ctx, sessionID, "tool_call_completed", logResult)
	record.Category, record.Level, record.Type, record.ContentLimit, record.Content = events.LogAudit, slog.LevelInfo, events.ContentJSON, 0, string(data)
	m.Log(ctx, record)
}
