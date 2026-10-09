package chatcompletions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"elbot/internal/contextinfo"
	globalevents "elbot/internal/events"
	"elbot/internal/llm"
)

func (a *Client) logChatRequest(ctx context.Context, req Request, bodyBytes []byte) {
	a.logFirstSystemMessage(ctx, req)
	// The request source supplies identity explicitly; EmitLog never fills it.
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	record := globalevents.LogRecord{
		Category:  globalevents.LogRuntime,
		Level:     slog.LevelDebug,
		EventName: "openai_chat_request",
		Module:    "model",
		Content: fmt.Sprintf("openai chat request: endpoint=%s model=%s %s\nlatest message: %s",
			a.endpoint(), req.Model, chatRequestLogSummary(req, bodyBytes), latestMessageJSON(req.Messages)),
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

func chatRequestLogSummary(req Request, bodyBytes []byte) string {
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
	return fmt.Sprintf("message_count=%d message_roles=%s latest_message=%s tool_count=%d system_hash=%s tools_hash=%s body_hash=%s",
		len(req.Messages), strings.Join(roles, ","), latest, len(req.Tools),
		hashText(firstSystemText(req.Messages)), hashJSON(req.Tools), hashBytes(bodyBytes))
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
