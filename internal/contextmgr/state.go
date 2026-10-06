package contextmgr

import (
	"context"
	"encoding/json"
	"fmt"

	"elbot/internal/llm"
	"elbot/internal/storage"
)

type CompactState struct {
	Pending         bool   `json:"pending,omitempty"`
	Summary         string `json:"summary,omitempty"`
	SeedID          string `json:"seed_id,omitempty"`
	SourceSessionID string `json:"source_session_id,omitempty"`
	FromMessageID   string `json:"from_message_id,omitempty"`
	ToMessageID     string `json:"to_message_id,omitempty"`
	Provider        string `json:"provider,omitempty"`
	Model           string `json:"model,omitempty"`
	TriggerReason   string `json:"trigger_reason,omitempty"`
	SourceTokens    int    `json:"source_tokens,omitempty"`
	SummaryTokens   int    `json:"summary_tokens,omitempty"`
	TotalTokens     int    `json:"total_tokens,omitempty"`
	CacheHitTokens  int    `json:"cache_hit_tokens,omitempty"`
	Generation      int    `json:"generation,omitempty"`
	BaseTitle       string `json:"base_title,omitempty"`
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
	var state State
	for key, target := range map[string]any{"last_usage": &state.LastUsage, "context_compact": &state.Compact} {
		if value, ok := fields[key]; ok {
			if err := json.Unmarshal(value, target); err != nil {
				return State{}, fmt.Errorf("decode context metadata %s: %w", key, err)
			}
		}
	}
	return state, nil
}

func PendingCompact(row *storage.Session) (*CompactState, error) {
	if row == nil {
		return nil, nil
	}
	state, err := DecodeState(row.Metadata)
	if err != nil {
		return nil, err
	}
	if state.Compact == nil || !state.Compact.Pending || state.Compact.Summary == "" && state.Compact.SeedID == "" {
		return nil, nil
	}
	return state.Compact, nil
}

func (s *Service) ConsumeSeed(ctx context.Context, id string) (*storage.Session, error) {
	return s.store.Sessions().Mutate(ctx, id, func(row *storage.Session) error {
		state, err := DecodeState(row.Metadata)
		if err != nil {
			return err
		}
		if state.Compact == nil || !state.Compact.Pending {
			return nil
		}
		state.Compact.Pending = false
		fields, err := storage.DecodeSessionMetadata(row.Metadata)
		if err != nil {
			return err
		}
		if err := fields.Set("context_compact", state.Compact); err != nil {
			return err
		}
		row.Metadata, err = fields.Encode()
		if err == nil {
			row.UpdatedAt = storage.Now()
		}
		return err
	})
}

// CompactedMetadata carries the latest other-module fields into the new session.
func CompactedMetadata(raw string, state *CompactState) (string, error) {
	if _, err := DecodeState(raw); err != nil {
		return "", err
	}
	fields, err := storage.DecodeSessionMetadata(raw)
	if err != nil {
		return "", err
	}
	if err := fields.Set("context_compact", state); err != nil {
		return "", err
	}
	delete(fields, "last_usage")
	delete(fields, "llm_checkpoint")
	delete(fields, "llm_seed")
	return fields.Encode()
}
