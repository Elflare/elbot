package agent

import (
	"context"

	"elbot/internal/llm"
	"elbot/internal/tool"
)

func (a *Agent) confirmCronSandboxShell(ctx context.Context, sessionID string, call llm.ToolCallRequest, risk tool.RiskLevel, message *llm.LLMMessage) (bool, bool) {
	sandbox, ok := tool.SandboxContextFromContext(ctx)
	if !ok || !sandbox.CronBackground || call.Name != "shell" {
		return false, false
	}
	if risk == tool.RiskCritical {
		a.audit("cron_shell_rejected", "session_id", sessionID, "tool", call.Name, "risk", risk, "sandbox_dir", sandbox.Dir, "arguments", previewArguments(call.Arguments))
		message.Segments = llm.TextSegments("cron 后台禁止执行 critical 风险 shell 命令。请改用相对路径或低风险命令；若必须执行，请在报告中提示用户 /resume 该 cron session 后人工确认。")
		return false, true
	}
	a.audit("cron_shell_auto_confirmed", "session_id", sessionID, "tool", call.Name, "risk", risk, "sandbox_dir", sandbox.Dir, "arguments", previewArguments(call.Arguments))
	return true, true
}
