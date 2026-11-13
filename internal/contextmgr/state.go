package contextmgr

import (
	"encoding/json"
	"fmt"

	"elbot/internal/llm"
	"elbot/internal/storage"
)

type CompactState struct {
	// Inputs retains original user messages separately from the generated summary message.
	Inputs          []string `json:"inputs,omitempty"`
	SourceSessionID string   `json:"source_session_id,omitempty"`
	FromMessageID   string   `json:"from_message_id,omitempty"`
	ToMessageID     string   `json:"to_message_id,omitempty"`
	Provider        string   `json:"provider,omitempty"`
	Model           string   `json:"model,omitempty"`
	TriggerReason   string   `json:"trigger_reason,omitempty"`
	// SourceTokens is the compactor's reported input count; nil means unknown.
	SourceTokens *int64 `json:"source_tokens,omitempty"`
	// SummaryTokens is the reported output count; a non-nil zero is explicit zero.
	SummaryTokens *int64 `json:"summary_tokens,omitempty"`
	// TotalTokens is the reported total; compactors do not infer missing totals.
	TotalTokens *int64 `json:"total_tokens,omitempty"`
	// CacheHitTokens is the reported cached count; nil remains unknown across saves.
	CacheHitTokens *int64 `json:"cache_hit_tokens,omitempty"`
	Generation     int    `json:"generation,omitempty"`
	BaseTitle      string `json:"base_title,omitempty"`
}

type State struct {
	LastUsage *llm.Usage
	Compact   *CompactState
}

func DecodeState(raw string) (State, error) {
	fields, err := storage.DecodeSessionMetadata(raw)
	if err != nil {
		return State{}, err
	}
	return decodeState(fields)
}
func decodeState(fields storage.SessionMetadata) (State, error) {
	var state State
	for key, target := range map[string]any{
		"last_usage":      &state.LastUsage,
		"context_compact": &state.Compact,
	} {
		if raw, ok := fields[key]; ok {
			if err := json.Unmarshal(raw, target); err != nil {
				return State{}, fmt.Errorf("decode context metadata %s: %w", key, err)
			}
		}
	}
	return state, nil
}

// CompactedMetadata carries the latest other-module fields into the new session.
func CompactedMetadata(raw string, state *CompactState) (string, error) {
	fields, err := storage.DecodeSessionMetadata(raw)
	if err != nil {
		return "", err
	}
	if _, err := decodeState(fields); err != nil {
		return "", err
	}
	if err := fields.Set("context_compact", state); err != nil {
		return "", err
	}
	delete(fields, "last_usage")
	delete(fields, "llm_checkpoint")
	delete(fields, "llm_seed")
	delete(fields, "elyph_rules_shown")
	return fields.Encode()
}
