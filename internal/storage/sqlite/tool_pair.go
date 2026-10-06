package sqlite

import (
	"encoding/json"
	"fmt"

	"elbot/internal/storage"
)

func validateToolPair(commit storage.DialogueCommit) error {
	pair := commit.ToolPair
	if pair == nil {
		return nil
	}
	if pair.Call == nil || pair.Result == nil || pair.Call.Role != storage.RoleAssistant || pair.Result.Role != storage.RoleTool || pair.Call.ID == "" || pair.Result.ID == "" || pair.Call.ID == pair.Result.ID {
		return fmt.Errorf("invalid tool pair messages")
	}
	fields, err := storage.DecodeSessionMetadata(pair.Call.Metadata)
	if err != nil {
		return err
	}
	var calls []struct{ ID string }
	if err := json.Unmarshal(fields["tool_calls"], &calls); err != nil {
		return err
	}
	id, err := storage.ToolResultMessageID(*pair.Call)
	if err != nil || id != pair.Result.ID || len(calls) != 1 || calls[0].ID == "" || calls[0].ID != pair.Result.ToolCallID {
		return fmt.Errorf("tool pair identity mismatch")
	}
	if pair.Call.CreatedAt.IsZero() {
		pair.Call.CreatedAt = storage.Now()
	}
	pair.Result.CreatedAt = pair.Call.CreatedAt
	return nil
}
