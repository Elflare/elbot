package runtime

import (
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	hookprotocol "elbot/internal/hook/protocol"
	"elbot/internal/llm"
)

const protocolVersion = hookprotocol.Version

type frame = hookprotocol.Frame

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
	hookprotocol.EventHandleParams
	Continuation bool   `json:"continuation,omitempty"`
	ToolContext  string `json:"tool_context"`
}

type eventResult struct {
	hookprotocol.EventResultBase
	ConversationID string    `json:"conversation_id,omitempty"`
	ExpiresAt      time.Time `json:"expires_at,omitempty"`
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
