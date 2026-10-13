package sqlite

import (
	"encoding/json"
	"fmt"

	"elbot/internal/storage"
)

func validateToolBatch(commit storage.DialogueCommit) error {
	batch := commit.ToolBatch
	if batch == nil {
		return nil
	}
	if batch.Call == nil || batch.Call.Role != storage.RoleAssistant || batch.Call.ID == "" || len(batch.Results) == 0 {
		return fmt.Errorf("invalid tool batch messages")
	}
	fields, err := storage.DecodeSessionMetadata(batch.Call.Metadata)
	if err != nil {
		return err
	}
	var calls []struct{ ID string }
	if err := json.Unmarshal(fields["tool_calls"], &calls); err != nil {
		return err
	}
	ids, err := storage.ToolResultMessageIDs(*batch.Call)
	if err != nil || len(calls) != len(batch.Results) || len(ids) != len(calls) {
		return fmt.Errorf("tool batch identity mismatch")
	}
	if batch.Call.CreatedAt.IsZero() {
		batch.Call.CreatedAt = storage.Now()
	}
	seen := map[string]bool{batch.Call.ID: true}
	callIDs := map[string]bool{}
	for i, result := range batch.Results {
		if result == nil || result.Role != storage.RoleTool || result.ID == "" || seen[result.ID] || ids[i] != result.ID || calls[i].ID == "" || callIDs[calls[i].ID] || calls[i].ID != result.ToolCallID {
			return fmt.Errorf("tool batch identity mismatch")
		}
		seen[result.ID], callIDs[calls[i].ID] = true, true
		result.CreatedAt = batch.Call.CreatedAt
	}
	return nil
}
