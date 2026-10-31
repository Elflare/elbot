package chatcompletions

import (
	"bytes"
	"context"
	"elbot/internal/logging"
	"encoding/json"
	"log/slog"
	"strings"

	"elbot/internal/contextinfo"
	globalevents "elbot/internal/events"
	"elbot/internal/llm"
)

func (a *Client) logChatRequest(ctx context.Context, req Request) {
	a.logFirstSystemMessage(ctx, req)
	// The request source supplies identity explicitly; EmitLog never fills it.
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	latest := ""
	if len(req.Messages) > 0 {
		message := req.Messages[len(req.Messages)-1]
		latest = string(message.Role)
		if message.Name != "" {
			latest += ":" + message.Name
		}
	}
	content, _ := json.Marshal(map[string]any{
		"endpoint": a.endpoint(), "model": req.Model,
		"message_count": len(req.Messages), "latest_message": latest, "tool_count": len(req.Tools),
		"message": latestLogMessage(req.Messages),
	})
	record := globalevents.LogRecord{
		Category:  globalevents.LogRuntime,
		Level:     slog.LevelDebug,
		EventName: "openai_chat_request",
		Module:    "model",
		Type:      globalevents.ContentJSON,
		Content:   string(content),
	}
	if req.SessionID != "" {
		record.Identity.SessionID = &req.SessionID
	}
	if facts.RunID != "" {
		record.Identity.RunID = &facts.RunID
	}
	if facts.Attempt != "" {
		record.Identity.Attempt = &facts.Attempt
	}
	if facts.RequestID != "" {
		record.Identity.RequestID = &facts.RequestID
	}
	if facts.RootRequestID != "" {
		record.Identity.RootRequestID = &facts.RootRequestID
	}
	_ = globalevents.EmitLog(ctx, record)
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

	facts, _ := contextinfo.ExecutionFromContext(ctx)
	record := globalevents.LogRecord{
		Category:  globalevents.LogRuntime,
		Level:     slog.LevelInfo,
		EventName: "system_message",
		Module:    "model",
		Content:   "system prompt: " + firstSystemText(req.Messages) + "\nmodel=" + req.Model,
		Identity:  globalevents.LogIdentity{SessionID: &req.SessionID},
	}
	if facts.RunID != "" {
		record.Identity.RunID = &facts.RunID
	}
	if facts.Attempt != "" {
		record.Identity.Attempt = &facts.Attempt
	}
	if facts.RequestID != "" {
		record.Identity.RequestID = &facts.RequestID
	}
	if facts.RootRequestID != "" {
		record.Identity.RootRequestID = &facts.RootRequestID
	}
	_ = globalevents.EmitLog(ctx, record)
}

func latestLogMessage(messages []llm.LLMMessage) *openAIMessage {
	if len(messages) == 0 {
		return nil
	}
	latest := toOpenAIMessages(logSafeMessages(messages[len(messages)-1:]))[0]
	return &latest
}

func logSafeMessages(messages []llm.LLMMessage) []llm.LLMMessage {
	out := llm.CloneMessages(messages)
	for i := range out {
		for j := range out[i].Segments {
			out[i].Segments[j].Text = logging.TruncateContent(out[i].Segments[j].Text, 256)
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

func firstSystemText(messages []llm.LLMMessage) string {
	for _, message := range messages {
		if message.Role == llm.RoleSystem {
			return llm.SegmentsContentText(message.Segments)
		}
	}
	return ""
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
