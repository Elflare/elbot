package chatcompletions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"strings"

	globalevents "elbot/internal/events"
	"elbot/internal/llm"
)

func (a *Client) logChatRequest(ctx context.Context, req Request, bodyBytes []byte) {
	a.logFirstSystemMessage(ctx, req)
	attrs := []any{"endpoint", a.endpoint(), "model", req.Model, "session_id", req.SessionID}
	// Keep only the latest message in DEBUG detail; never publish full history.
	attrs = append(attrs, chatRequestLogSummary(req, bodyBytes)...)
	_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
		Category: globalevents.LogRuntime,
		Level:    slog.LevelDebug,
		Name:     "openai_chat_request",
		Module:   "model",
		Summary:  "openai chat request",
		Detail:   latestMessageJSON(req.Messages),
		Fields:   slog.Group("", attrs...).Value.Group(),
	})
}

func (a *Client) logFirstSystemMessage(ctx context.Context, req Request) {
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

	_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
		Category: globalevents.LogRuntime,
		Level:    slog.LevelInfo,
		Name:     "system_message",
		Module:   "model",
		Summary:  "system prompt: " + firstSystemText(req.Messages),
		Fields:   []slog.Attr{slog.Any("session_id", req.SessionID), slog.Any("model", req.Model)},
		Detail:   firstSystemMessageJSON(req.Messages),
	})
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
