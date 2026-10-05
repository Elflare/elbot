package agent

import (
	"context"

	"elbot/internal/llm"
	sandboxctx "elbot/internal/sandbox"
	"elbot/internal/tool"
)

func (c *confirmationCoordinator) confirmBackgroundSandboxShell(ctx context.Context, sessionID string, call llm.ToolCallRequest, risk tool.RiskLevel, message *llm.LLMMessage) (bool, bool) {
	sandbox, ok := sandboxctx.SandboxContextFromContext(ctx)
	if !ok || !sandbox.Background || call.Name != "shell" {
		return false, false
	}
	if sandbox.BackgroundKind != sandboxctx.BackgroundKindCron && sandbox.BackgroundKind != sandboxctx.BackgroundKindElnis {
		return false, false
	}
	kind := string(sandbox.BackgroundKind)
	event := ConfirmationChangedEvent{EventMeta: eventMeta(ctx, sessionID), Phase: "background", Action: "auto_confirmed", Kind: kind, Tool: call.Name, Risk: string(risk), SandboxDir: sandbox.Dir, Arguments: call.Arguments}
	if risk == tool.RiskCritical {
		event.Action = "rejected"
		emitFact(ctx, c.changed, event)
		message.Segments = llm.TextSegments("后台沙盒禁止执行 critical 风险 shell 命令。请改用相对路径，把文件限制在当前 sandbox 目录内，避免绝对路径、.. 逃逸和危险命令；若仍无法完成，请在最终报告中说明阻塞原因。")
		return false, true
	}
	emitFact(ctx, c.changed, event)
	return true, true
}
