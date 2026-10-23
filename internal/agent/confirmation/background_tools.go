package confirmation

import (
	"context"
	"encoding/json"
	"log/slog"

	agentevents "elbot/internal/agent/events"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/events"
	"elbot/internal/llm"
	sandboxctx "elbot/internal/sandbox"
	"elbot/internal/tool"
)

func (c *ConfirmationCoordinator) ConfirmBackgroundSandboxShell(ctx context.Context, sessionID string, call llm.ToolCallRequest, risk tool.RiskLevel, message *llm.LLMMessage) (bool, bool) {
	sandbox, ok := sandboxctx.SandboxContextFromContext(ctx)
	if !ok || !sandbox.Background || call.Name != "shell" {
		return false, false
	}
	if sandbox.BackgroundKind != sandboxctx.BackgroundKindCron && sandbox.BackgroundKind != sandboxctx.BackgroundKindElnis {
		return false, false
	}
	kind := string(sandbox.BackgroundKind)
	content := map[string]string{"kind": kind, "tool": call.Name, "risk": string(risk), "sandbox_dir": sandbox.Dir, "arguments": call.Arguments}
	for key, value := range content {
		runes := []rune(value)
		if len(runes) > 2048 {
			content[key] = string(runes[:2047]) + "…"
		}
	}
	data, _ := json.Marshal(content)
	record := events.LogRecord{
		Category:     events.LogAudit,
		Level:        slog.LevelInfo,
		EventName:    "background_shell_auto_confirmed",
		Module:       "agent",
		Type:         events.ContentJSON,
		Content:      string(data),
		ContentLimit: 0,
		Identity:     events.LogIdentity{},
		ResultStatus: events.ResultSucceeded,
	}
	if risk == tool.RiskCritical {
		record.EventName, record.ResultStatus = "background_shell_rejected", events.ResultRejected
		agentshare.EmitAgentLog(ctx, agentevents.Meta(ctx, sessionID), record)
		message.Segments = llm.TextSegments("后台沙盒禁止执行 critical 风险 shell 命令。请改用相对路径，把文件限制在当前 sandbox 目录内，避免绝对路径、.. 逃逸和危险命令；若仍无法完成，请在最终报告中说明阻塞原因。")
		return false, true
	}
	agentshare.EmitAgentLog(ctx, agentevents.Meta(ctx, sessionID), record)
	return true, true
}
