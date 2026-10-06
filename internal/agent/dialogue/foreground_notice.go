package dialogue

import (
	"elbot/internal/llm"
	"elbot/internal/session"
	"elbot/internal/storage"
)

// ForegroundNotice is appended to the request tail as a synthetic user message;
// it is never inserted into the system prompt or attributed to a platform user.
func ForegroundNotice(row *storage.Session) (llm.LLMMessage, bool) {
	if row == nil || !session.WasPromoted(row) {
		return llm.LLMMessage{}, false
	}
	return llm.LLMMessage{Role: llm.RoleUser, Segments: llm.TextSegments("[系统提示]\n此会话已由用户接管，当前是普通前台对话。保留原任务目标和历史，但后台无人值守、自动汇报及强制 JSON 输出要求已经解除；按当前用户要求正常回复，工具遵循前台权限和确认规则。")}, true
}
