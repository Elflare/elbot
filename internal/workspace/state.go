package workspace

import (
	"encoding/json"
	"fmt"
	"strings"
)

// State owns only workspace metadata; the Session adapter preserves other keys.
type State struct {
	Dir             string   `json:"workspace_dir,omitempty"`
	AgentNoticeDirs []string `json:"workspace_agent_notice_dirs,omitempty"`
}

func DecodeState(raw string) (State, error) {
	var state State
	if strings.TrimSpace(raw) == "" {
		return state, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return state, fmt.Errorf("decode workspace metadata: %w", err)
	}
	if fields == nil {
		return state, fmt.Errorf("workspace metadata must be an object")
	}
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return state, fmt.Errorf("decode workspace metadata: %w", err)
	}
	state.Dir = strings.TrimSpace(state.Dir)
	return state, nil
}
