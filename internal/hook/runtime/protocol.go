package runtime

import (
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"elbot/internal/delivery"
	"elbot/internal/hook"
	"elbot/internal/llm"
)

const protocolVersion = "hook.v2"

type frame struct {
	Type   string          `json:"type"`
	ID     string          `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	OK     *bool           `json:"ok,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

type systemInit struct {
	Version string           `json:"version"`
	Hook    systemHook       `json:"hook"`
	Tools   []llm.ToolSchema `json:"tools"`
}

type systemHook struct {
	ID          string `json:"id"`
	Description string `json:"description,omitempty"`
	Dir         string `json:"plugin_dir"`
	Cwd         string `json:"cwd"`
	SharedDir   string `json:"shared_dir"`
}

type eventHandle struct {
	Event        hook.Event        `json:"event"`
	Match        hook.MatchContext `json:"match,omitempty"`
	Continuation bool              `json:"continuation,omitempty"`
	ToolContext  string            `json:"tool_context"`
}

func eventMatch(event hook.Event) hook.MatchContext {
	if event.Metadata == nil {
		return hook.MatchContext{}
	}
	match, _ := event.Metadata["match"].(hook.MatchContext)
	return match
}

type eventResult struct {
	Status         string       `json:"status"`
	ConversationID string       `json:"conversation_id,omitempty"`
	ExpiresAt      time.Time    `json:"expires_at,omitempty"`
	Outputs        []outputSpec `json:"outputs,omitempty"`
	PassThrough    *bool        `json:"pass_through,omitempty"`
}

type outputSpec struct {
	Kind             string          `json:"kind"`
	Text             string          `json:"text,omitempty"`
	Name             string          `json:"name,omitempty"`
	AltText          string          `json:"alt_text,omitempty"`
	URL              string          `json:"url,omitempty"`
	Path             string          `json:"path,omitempty"`
	MIMEType         string          `json:"mime_type,omitempty"`
	ReplyToMessageID string          `json:"reply_to_message_id,omitempty"`
	Target           delivery.Target `json:"target,omitempty"`
}

type stderrLogger struct {
	logger *slog.Logger
	hookID string
}

func (l stderrLogger) Write(data []byte) (int, error) {
	if l.logger != nil {
		line := strings.TrimSpace(string(data))
		if line != "" {
			l.logger.Info("stateful hook stderr", "hook", l.hookID, "line", line)
		}
	}
	return len(data), nil
}

func mustJSON(value any) json.RawMessage {
	data, _ := json.Marshal(value)
	return data
}
