package chatcompletions

import (
	"bytes"
	"crypto/sha256"
	"elbot/internal/llm"
	"encoding/hex"
	"encoding/json"
	"strings"
)

func (a *Client) logChatRequest(req Request, bodyBytes []byte) {
	if a.logger == nil {
		return
	}
	a.logFirstSystemMessage(req)
	attrs := []any{"endpoint", a.endpoint(), "model", req.Model, "session_id", req.SessionID, "latest_message_json", latestMessageJSON(req.Messages)}
	// Debug 日志默认只记录请求摘要，不记录 Authorization 和完整 body。
	// 完整 body 可能包含用户正文、图片 URL、工具参数等敏感信息，
	// 需要临时排查时再手动打开。
	// attrs := []any{"endpoint", a.endpoint(), "model", req.Model, "body_json", string(bodyBytes)}
	attrs = append(attrs, chatRequestLogSummary(req, bodyBytes)...)
	a.logger.Debug("openai chat request", attrs...)
}

func (a *Client) logFirstSystemMessage(req Request) {
	if req.SessionID == "" || firstSystemText(req.Messages) == "" {
		return
	}
	a.loggedSystemMu.Lock()
	if a.loggedSystem[req.SessionID] {
		a.loggedSystemMu.Unlock()
		return
	}
	a.loggedSystem[req.SessionID] = true
	a.loggedSystemMu.Unlock()

	a.logger.Info("system prompt",
		"event", "system_message",
		"session_id", req.SessionID,
		"model", req.Model,
		"first_system_message_json", firstSystemMessageJSON(req.Messages),
	)
}

func latestMessageJSON(messages []llm.LLMMessage) string {
	if len(messages) == 0 {
		return ""
	}
	latest := toOpenAIMessages(logSafeMessages(messages[len(messages)-1:]))
	data, err := marshalJSONNoEscape(latest[0])
	if err != nil {
		return ""
	}
	return string(data)
}

func firstSystemMessageJSON(messages []llm.LLMMessage) string {
	for _, message := range messages {
		if message.Role != llm.RoleSystem {
			continue
		}
		converted := toOpenAIMessages(logSafeMessages([]llm.LLMMessage{message}))
		data, err := marshalJSONNoEscape(converted[0])
		if err != nil {
			return ""
		}
		return string(data)
	}
	return ""
}

func logSafeMessages(messages []llm.LLMMessage) []llm.LLMMessage {
	out := llm.CloneMessages(messages)
	for i := range out {
		for j := range out[i].Segments {
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(out[i].Segments[j].URL)), "data:") {
				out[i].Segments[j].URL = redactDataURL(out[i].Segments[j].URL)
			}
		}
	}
	return out
}

func redactDataURL(value string) string {
	value = strings.TrimSpace(value)
	if comma := strings.IndexByte(value, ','); comma >= 0 {
		return value[:comma] + ",…"
	}
	if len(value) > 128 {
		return value[:128] + "…"
	}
	return value
}

func chatRequestLogSummary(req Request, bodyBytes []byte) []any {
	roles := make([]string, 0, len(req.Messages))
	for _, message := range req.Messages {
		role := string(message.Role)
		if message.Name != "" {
			role += ":" + message.Name
		}
		roles = append(roles, role)
	}
	latest := ""
	if len(roles) > 0 {
		latest = roles[len(roles)-1]
	}
	return []any{
		"message_count", len(req.Messages),
		"message_roles", strings.Join(roles, ","),
		"latest_message", latest,
		"tool_count", len(req.Tools),
		"system_hash", hashText(firstSystemText(req.Messages)),
		"tools_hash", hashJSON(req.Tools),
		"body_hash", hashBytes(bodyBytes),
	}
}

func firstSystemText(messages []llm.LLMMessage) string {
	for _, message := range messages {
		if message.Role == llm.RoleSystem {
			return llm.SegmentsContentText(message.Segments)
		}
	}
	return ""
}

func hashJSON(value any) string {
	data, err := marshalJSONNoEscape(value)
	if err != nil {
		return ""
	}
	return hashBytes(data)
}

func hashText(text string) string {
	return hashBytes([]byte(text))
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

func marshalJSONNoEscape(value any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSpace(buf.Bytes()), nil
}
